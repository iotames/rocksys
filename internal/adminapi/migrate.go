// migrate.go：数据迁移与目标库表结构对齐端点。
//
// 换库/数据汇聚场景：本机运行库（或另一外部数据源）→ 外部数据源 的结构与数据搬运。
//
//	GET  /admin/db/migrate/schema        —— 目标库结构差异预览（?code=<数据源 Code>，只读）
//	POST /admin/db/migrate/schema_apply  —— 对目标库执行对齐 DDL（后台任务，提交即返回任务 ID）
//	POST /admin/db/migrate/start         —— 启动数据迁移（后台任务，表级进度/取消）
//	GET  /admin/db/migrate/status        —— 迁移任务状态（表级明细便捷视图）
//	POST /admin/db/migrate/cancel        —— 取消（带 table= 取消单张待迁移表；不带 = 整任务）
//
// 目标库连接每次按需打开、不注入外挂脚本内容中枢：中枢对同名子目录只允许注册一次，
// 运行库启动时已注册 sql/，目标库复用同一实例必然失败；不注入时脚本经内嵌/外挂目录直读，
// 与运行库读同一份脚本文件，读取语义同源（外挂优先、内嵌兜底），仅无缓存与热更订阅——
// 对低频运维动作无影响。
//
// 审计边界：对目标库的动作不属于运行库审计域，不写 sql_exec_log；结果在任务进度与
// 响应中展示。
package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/iotames/easydb/dsn"
	"github.com/iotames/easyserver/log"

	"rocksys/internal/db"
	"rocksys/internal/taskcenter"
)

// 数据迁移端点路径。
const (
	PathMigrateSchema      = "/admin/db/migrate/schema"
	PathMigrateSchemaApply = "/admin/db/migrate/schema_apply"
	PathMigrateStart       = "/admin/db/migrate/start"
	PathMigrateStatus      = "/admin/db/migrate/status"
	PathMigrateCancel      = "/admin/db/migrate/cancel"
)

// openTargetDB 按数据源 Code 打开目标库连接（调用方负责 Close）。
// 每次对齐/迁移动作独立打开：目标库为低频运维对象，不复用连接池、不注册脚本中枢。
func (s *AdminServer) openTargetDB(code string) (*db.DB, error) {
	ds, ok := s.getDsnByCode(code)
	if !ok {
		return nil, fmt.Errorf("数据源不存在（Code=%s）：请刷新数据源列表确认最新状态", code)
	}
	return db.Open(ds.DriverName, ds.Dsn)
}

// handleMigrateSchema 目标库结构差异预览：期望（运行库同款 SQL 脚本源）vs 实际（目标库 catalog），
// 返回差异项与生成的对齐 DDL 文本（只读，不执行）。
func (s *AdminServer) handleMigrateSchema(w http.ResponseWriter, r *http.Request) {
	if len(s.tableSpecs) == 0 {
		http.Error(w, "表结构对齐不可用：期望结构表清单未装配", http.StatusServiceUnavailable)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "缺少 code 查询参数（目标数据源 Code，见数据源卡列表）", http.StatusBadRequest)
		return
	}
	target, err := s.openTargetDB(code)
	if err != nil {
		http.Error(w, "打开目标库失败："+err.Error(), http.StatusBadRequest)
		return
	}
	defer target.Close()
	items, err := db.DiffSchema(r.Context(), target, s.tableSpecs)
	if err != nil {
		http.Error(w, "目标库结构检查失败："+err.Error()+"；请确认目标库连接正常后重试", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []db.DiffItem{}
	}
	sqlText := ""
	if len(items) > 0 {
		if sqlText, err = db.GenerateSQL(items, s.tableSpecs, target); err != nil {
			http.Error(w, "生成对齐 SQL 失败："+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	_ = writeJSON(w, map[string]any{
		"driver": target.Driver(),
		"items":  items,
		"sql":    sqlText,
	}, http.StatusOK)
}

// schemaApplyResult 逐条 DDL 执行结果（进度明细透传，前端渲染）。
type schemaApplyResult struct {
	Seq   int    `json:"seq"`
	SQL   string `json:"sql"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// handleMigrateSchemaApply 对目标库执行对齐 DDL：一律后台任务化（长 DDL 必超 HTTP 超时），
// 提交即返回任务 ID；拆句逐条执行、遇错即停，逐条结果经任务进度/结果查询取回。
func (s *AdminServer) handleMigrateSchemaApply(w http.ResponseWriter, r *http.Request) {
	if len(s.tableSpecs) == 0 {
		http.Error(w, "表结构对齐不可用：期望结构表清单未装配", http.StatusServiceUnavailable)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "缺少 code 查询参数（目标数据源 Code）", http.StatusBadRequest)
		return
	}
	// 提交前预读差异：空 diff 直接返回免任务；非空把 DDL 带进任务（任务执行不依赖请求上下文）。
	target, err := s.openTargetDB(code)
	if err != nil {
		http.Error(w, "打开目标库失败："+err.Error(), http.StatusBadRequest)
		return
	}
	items, err := db.DiffSchema(r.Context(), target, s.tableSpecs)
	if err != nil {
		_ = target.Close()
		http.Error(w, "目标库结构检查失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(items) == 0 {
		_ = target.Close()
		_ = writeJSON(w, map[string]any{"ok": true, "noop": true, "message": "目标库结构与期望一致，无需对齐"}, http.StatusOK)
		return
	}
	sqlText, err := db.GenerateSQL(items, s.tableSpecs, target)
	_ = target.Close()
	if err != nil {
		http.Error(w, "生成对齐 SQL 失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	stmts := db.SplitStatements(sqlText)

	ds, _ := s.getDsnByCode(code)
	taskID, err := s.SubmitTask(taskcenter.Spec{
		CreatedBy: "schema_apply",
		Title:     fmt.Sprintf("表结构对齐 → %s（%s）", ds.Name, ds.DriverName),
		Run: func(ctx context.Context, setProgress taskcenter.SetProgressFn) error {
			return runSchemaApply(ctx, ds.DriverName, ds.Dsn, stmts, setProgress)
		},
	})
	if err != nil {
		http.Error(w, "提交对齐任务失败："+err.Error(), http.StatusConflict)
		return
	}
	writeTaskRefJSON(w, taskID, map[string]any{"total": len(stmts)})
}

// runSchemaApply 逐条执行对齐 DDL：遇错即停（DDL 无跨方言统一事务语义，前序已生效不可回滚），
// 结果归入任务进度（逐条明细）与 Result（摘要）。执行期重开目标库连接（任务生命周期与
// HTTP 请求解耦，不能复用请求作用域连接）。
func runSchemaApply(ctx context.Context, driver, dsnStr string, stmts []string, setProgress taskcenter.SetProgressFn) error {
	target, err := db.Open(driver, dsnStr)
	if err != nil {
		return fmt.Errorf("打开目标库失败: %w", err)
	}
	defer target.Close()

	results := make([]schemaApplyResult, 0, len(stmts))
	executed := 0
	for i, stmt := range stmts {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("任务已取消（已执行 %d/%d 条，前序已生效不可回滚）", executed, len(stmts))
		}
		item := schemaApplyResult{Seq: i + 1, SQL: stmt}
		if _, err := target.EasyDB().GetSqlDB().ExecContext(ctx, stmt); err != nil {
			// 索引幂等容错：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
			// "Duplicate key name"；"already exists" 覆盖 sqlite/PG 已存在对象场景。
			// 与 obs/mq/execlogstore 的建索引容错同口径（仅索引/已存在语义被忽略，
			// 其余错误照旧遇错即停）。
			if isAlreadyExistsErr(err) {
				item.OK = true
				results = append(results, item)
				executed++
				setProgress(&taskcenter.Progress{
					Text:   fmt.Sprintf("已执行 %d/%d 条（含已存在对象跳过）", executed, len(stmts)),
					Detail: results,
				})
				continue
			}
			item.Error = err.Error()
			results = append(results, item)
			setProgress(&taskcenter.Progress{
				Text:   fmt.Sprintf("第 %d/%d 条执行失败：%s", i+1, len(stmts), err.Error()),
				Detail: results,
			})
			log.Warn("migrate: 表结构对齐 DDL 执行失败（前序已生效）", "seq", i+1, "err", err.Error())
			return fmt.Errorf("第 %d 条执行失败：%s。前面 %d 条已生效且不可回滚；请修正后仅重新对齐剩余差异", i+1, err.Error(), executed)
		}
		item.OK = true
		results = append(results, item)
		executed++
		setProgress(&taskcenter.Progress{
			Text:   fmt.Sprintf("已执行 %d/%d 条", executed, len(stmts)),
			Detail: results,
		})
	}
	return nil
}

// isAlreadyExistsErr 判定"对象已存在"类幂等错误（重复建索引/建表）。
func isAlreadyExistsErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "Duplicate key name") ||
		strings.Contains(msg, "duplicate key")
}

// writeTaskRefJSON 输出任务引用响应（统一字段名 task_id，前端提交→轮询组件按此解析）。
func writeTaskRefJSON(w http.ResponseWriter, taskID string, extra map[string]any) {
	payload := map[string]any{"ok": true, "task_id": taskID}
	for k, v := range extra {
		payload[k] = v
	}
	_ = writeJSON(w, payload, http.StatusOK)
}

// ── 数据迁移执行器（表输入 → 表输出，批式搬运：流式读 → 攒批 → 批量 INSERT）─────────

const (
	// migrateDefaultBatch 默认每批行数（性能参考值：批越大吞吐越高，内存与目标库单语句负载越高；
	// 仅存任务内存态，重启恢复默认，不落盘、不注册配置项）。
	migrateDefaultBatch = 1000
	// migrateBatchMin/Max 批次服务端 clamp 边界。
	migrateBatchMin = 100
	migrateBatchMax = 10000
	// migratePlaceholderBudget 单条参数化 INSERT 的占位符预算（三方言通用、留余量）：
	// 占位符数 = 列数 × 行数，三方言硬上限 SQLite 32766 / PG·MySQL 65535，批次过大整批报错，
	// 且单纯下调批次治不了标（33 列 × 1000 行即超 SQLite）。执行器按预算把每批自适应切成
	// 多个子批（子批共享同一事务，事务边界/取消/进度语义不变），用户无感知。
	migratePlaceholderBudget = 30000
)

// 冲突策略。
const (
	migrateModeReplace = "replace" // 清空重灌（默认）
	migrateModeSkip    = "skip"    // 跳过冲突行
)

// 表级迁移状态。
const (
	tablePending   = "pending"
	tableRunning   = "running"
	tableDone      = "done"
	tableFailed    = "failed"
	tableCancelled = "cancelled"
)

// tableProgress 单表迁移进度（进度快照明细元素，快照只读）。
type tableProgress struct {
	Table     string `json:"table"`
	Status    string `json:"status"`
	RowsDone  int64  `json:"rows_done"`
	RowsTotal int64  `json:"rows_total"`
	Err       string `json:"err,omitempty"`
}

// migrateRunState 一次迁移任务的运行态：表级进度 + 取消标记（端点与任务 goroutine 共享）。
type migrateRunState struct {
	mu        sync.Mutex
	tables    []*tableProgress
	taskID    string // 所属任务 ID（整任务取消经任务中心送达）
	cancelled bool   // 整任务取消请求（当前批事务完成后停止）
}

// find 按表名取进度条目（调用方须持锁）。
func (st *migrateRunState) find(name string) *tableProgress {
	for _, t := range st.tables {
		if t.Table == name {
			return t
		}
	}
	return nil
}

// setStatus 原子改表状态。
func (st *migrateRunState) setStatus(name, status string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if t := st.find(name); t != nil {
		t.Status = status
	}
}

// markRemainingCancelled 从指定表起（含）把所有 pending 表置 cancelled（整任务取消收尾）。
func (st *migrateRunState) markRemainingCancelled(fromTable string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	started := false
	for _, t := range st.tables {
		if t.Table == fromTable {
			started = true
		}
		if started && t.Status == tablePending {
			t.Status = tableCancelled
		}
	}
}

// isCancelled 整任务取消标记。
func (st *migrateRunState) isCancelled() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cancelled
}

// cancelTable 单表取消：仅对 pending 生效（从本次任务移除）。
func (st *migrateRunState) cancelTable(name string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	t := st.find(name)
	if t == nil || t.Status != tablePending {
		return false
	}
	t.Status = tableCancelled
	return true
}

// snapshot 当前表级进度快照（整体替换语义，读侧拿一致视图）。
func (st *migrateRunState) snapshot() []tableProgress {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]tableProgress, 0, len(st.tables))
	for _, t := range st.tables {
		out = append(out, *t)
	}
	return out
}

// summaryText 汇总进度摘要（如「3/10 表完成，当前 access_log 500/12000 行」）。
func (st *migrateRunState) summaryText(current string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	done, total := 0, len(st.tables)
	var cur *tableProgress
	for _, t := range st.tables {
		if t.Status == tableDone {
			done++
		}
		if t.Table == current {
			cur = t
		}
	}
	s := fmt.Sprintf("%d/%d 表完成", done, total)
	if cur != nil && cur.Status == tableRunning {
		s += fmt.Sprintf("，当前 %s %d/%d 行", cur.Table, cur.RowsDone, cur.RowsTotal)
	}
	return s
}

// migrateRequest 数据迁移启动请求。
type migrateRequest struct {
	Source string   `json:"source"` // "self"（或缺省）= 本机运行库；否则为数据源 Code
	Target string   `json:"target"` // 数据源 Code（不允许指向运行库）
	Tables []string `json:"tables"` // 迁移表清单（期望结构业务表的子集，非空）
	Batch  int      `json:"batch"`  // 每批行数（默认 1000，服务端 clamp；会话内存态不落盘）
	Mode   string   `json:"mode"`   // replace = 清空重灌（默认）；skip = 跳过冲突行
}

// migrateParams 任务期参数快照（提交时定格，任务执行不依赖请求对象）。
type migrateParams struct {
	source   string
	targetDS dsn.DataSource
	tables   []string
	batch    int
	mode     string
	selfDB   *db.DB
}

// handleMigrateStart 启动数据迁移：校验 → 经任务中心提交（全局互斥由中心承担）→ 立即返回任务 ID。
func (s *AdminServer) handleMigrateStart(w http.ResponseWriter, r *http.Request) {
	if s.dataDB == nil || len(s.tableSpecs) == 0 {
		http.Error(w, "数据迁移不可用：运行库数据连接或期望结构表清单未装配", http.StatusServiceUnavailable)
		return
	}
	var req migrateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `请求体须为 {"source","target","tables","batch","mode"} JSON`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Target) == "" || req.Target == "self" {
		http.Error(w, "目标必须是外部数据源：禁止以本机运行库为迁移目标（防误覆盖生产数据）；请先在数据源卡添加目标库", http.StatusBadRequest)
		return
	}
	targetDS, ok := s.getDsnByCode(req.Target)
	if !ok {
		http.Error(w, "目标数据源不存在（Code="+req.Target+"）：请刷新数据源列表确认最新状态", http.StatusBadRequest)
		return
	}
	if req.Source != "" && req.Source != "self" {
		if _, ok := s.getDsnByCode(req.Source); !ok {
			http.Error(w, "源数据源不存在（Code="+req.Source+"）", http.StatusBadRequest)
			return
		}
	}
	mode := req.Mode
	if mode == "" {
		mode = migrateModeReplace
	}
	if mode != migrateModeReplace && mode != migrateModeSkip {
		http.Error(w, "mode 仅支持 replace（清空重灌）/ skip（跳过冲突行）", http.StatusBadRequest)
		return
	}
	batch := req.Batch
	if batch == 0 {
		batch = migrateDefaultBatch
	}
	if batch < migrateBatchMin {
		batch = migrateBatchMin
	}
	if batch > migrateBatchMax {
		batch = migrateBatchMax
	}
	// 表清单 = 请求表 ∩ 期望结构业务表（源库中不在期望结构的表不参与迁移，页面文案注明）。
	specTables := map[string]bool{}
	for _, sp := range s.tableSpecs {
		specTables[sp.Table] = true
	}
	var picked []string
	seen := map[string]bool{}
	for _, name := range req.Tables {
		if specTables[name] && !seen[name] {
			picked = append(picked, name)
			seen[name] = true
		}
	}
	if len(picked) == 0 {
		http.Error(w, "迁移表清单为空：所选表均不在期望结构业务表内；可选表清单来自表结构同步覆盖的业务表", http.StatusBadRequest)
		return
	}

	// 运行态 + 任务提交（任务中心全局互斥：有任务在跑直接拒绝，不排队）。
	st := &migrateRunState{}
	for _, name := range picked {
		st.tables = append(st.tables, &tableProgress{Table: name, Status: tablePending})
	}
	srcLabel := "本机运行库"
	if req.Source != "" && req.Source != "self" {
		if srcDS, ok := s.getDsnByCode(req.Source); ok {
			srcLabel = srcDS.Name
		}
	}
	params := migrateParams{
		source: req.Source, targetDS: targetDS, tables: picked,
		batch: batch, mode: mode, selfDB: s.dataDB,
	}
	taskID, err := s.SubmitTask(taskcenter.Spec{
		CreatedBy: "migrate",
		Title:     fmt.Sprintf("数据迁移 %s → %s（%d 表 × %d 行/批，%s）", srcLabel, targetDS.Name, len(picked), batch, mode),
		Run: func(ctx context.Context, setProgress taskcenter.SetProgressFn) error {
			return s.runMigration(ctx, params, st, setProgress)
		},
	})
	if err != nil {
		http.Error(w, "提交迁移任务失败："+err.Error(), http.StatusConflict)
		return
	}
	st.mu.Lock()
	st.taskID = taskID
	st.mu.Unlock()
	s.migMu.Lock()
	s.migState = st
	s.migMu.Unlock()
	writeTaskRefJSON(w, taskID, map[string]any{"tables": picked, "batch": batch, "mode": mode})
}

// sourceConn 打开源连接：self = 复用运行库连接（只读查询，不 Close），否则按 Code 打开（调用方 Close）。
func (s *AdminServer) sourceConn(p migrateParams) (*db.DB, func(), error) {
	if p.source == "" || p.source == "self" {
		return p.selfDB, func() {}, nil
	}
	ds, ok := s.getDsnByCode(p.source)
	if !ok {
		return nil, func() {}, fmt.Errorf("源数据源不存在（Code=%s）", p.source)
	}
	conn, err := db.Open(ds.DriverName, ds.Dsn)
	if err != nil {
		return nil, func() {}, fmt.Errorf("打开源库失败: %w", err)
	}
	return conn, func() { _ = conn.Close() }, nil
}

// runMigration 迁移主流程：逐表「清空（replace）→ 流式读 → 攒批写 → 自增序列重置」；
// 单表失败不阻塞后续表（结果汇总回显）；整任务取消在当前批事务完成后停止，剩余表置 cancelled。
func (s *AdminServer) runMigration(ctx context.Context, p migrateParams, st *migrateRunState, setProgress taskcenter.SetProgressFn) error {
	src, closeSrc, err := s.sourceConn(p)
	if err != nil {
		return err
	}
	defer closeSrc()
	tgt, err := db.Open(p.targetDS.DriverName, p.targetDS.Dsn)
	if err != nil {
		return fmt.Errorf("打开目标库失败: %w", err)
	}
	defer tgt.Close()

	// 预查各表总行数（进度分母；查询失败不阻断，分母留 0）。
	for _, t := range st.tables {
		var total int64
		if err := src.EasyDB().GetSqlDB().QueryRowContext(ctx, countSQL(src.Driver(), t.Table)).Scan(&total); err == nil {
			t.RowsTotal = total
		}
	}

	failed := 0
	for _, t := range st.tables {
		if ctx.Err() != nil || st.isCancelled() {
			st.markRemainingCancelled(t.Table)
			break
		}
		if t.Status != tablePending { // 已被单表取消
			continue
		}
		st.setStatus(t.Table, tableRunning)
		setProgress(&taskcenter.Progress{Text: st.summaryText(t.Table), Detail: st.snapshot()})
		rowsDone, err := s.migrateTable(ctx, src, tgt, t.Table, p, st, setProgress)
		st.mu.Lock()
		t.RowsDone = rowsDone
		st.mu.Unlock()
		switch {
		case err == nil:
			st.setStatus(t.Table, tableDone)
		case ctx.Err() != nil || st.isCancelled():
			st.setStatus(t.Table, tableCancelled)
		default:
			st.mu.Lock()
			t.Status = tableFailed
			t.Err = err.Error()
			st.mu.Unlock()
			failed++
		}
		setProgress(&taskcenter.Progress{Text: st.summaryText(t.Table), Detail: st.snapshot()})
	}
	if failed > 0 {
		return fmt.Errorf("迁移结束（%d 张表失败，详见进度明细）", failed)
	}
	return nil
}

// errMigrateCancelled 整任务取消的内部哨兵（表状态由外层统一置 cancelled）。
var errMigrateCancelled = fmt.Errorf("任务已取消")

// migrateTable 单表搬运：流式 SELECT → 攒批（batch=事务粒度）→ 子批参数化 INSERT（占位符预算切分）。
// 返回已迁移行数与错误。
func (s *AdminServer) migrateTable(ctx context.Context, src, tgt *db.DB, table string, p migrateParams, st *migrateRunState, setProgress taskcenter.SetProgressFn) (int64, error) {
	// 目标列集合：源有而目标无的列 → 该表失败并提示先结构对齐（不做自动建列）。
	tgtCols, err := targetColumns(ctx, tgt, table)
	if err != nil {
		return 0, fmt.Errorf("读取目标表 %s 列失败: %w", table, err)
	}
	rows, err := src.EasyDB().GetSqlDB().QueryContext(ctx, selectAllSQL(src.Driver(), table))
	if err != nil {
		return 0, fmt.Errorf("读取源表失败: %w", err)
	}
	defer rows.Close()
	srcCols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("读取源表列失败: %w", err)
	}
	// 同名列直迁（不同方言间按列名对齐，类型经 database/sql 原生传递由驱动双方自适应）。
	var writeCols []string
	for _, c := range srcCols {
		for _, tc := range tgtCols {
			if strings.EqualFold(c, tc) {
				writeCols = append(writeCols, c)
				break
			}
		}
	}
	if len(writeCols) < len(srcCols) {
		for _, c := range srcCols {
			found := false
			for _, tc := range tgtCols {
				if strings.EqualFold(c, tc) {
					found = true
					break
				}
			}
			if !found {
				return 0, fmt.Errorf("目标表缺列 %s；请先执行表结构对齐", c)
			}
		}
	}

	// replace：写前清空目标表（MySQL/PG TRUNCATE、SQLite DELETE）。
	if p.mode == migrateModeReplace {
		if _, err := tgt.EasyDB().GetSqlDB().ExecContext(ctx, truncateSQL(tgt.Driver(), table)); err != nil {
			return 0, fmt.Errorf("清空目标表失败: %w", err)
		}
	}

	insertStmt := buildInsertSQL(tgt.Driver(), table, writeCols, p.mode)
	conflictSuffix := insertConflictSuffix(tgt.Driver(), p.mode)
	subRows := migratePlaceholderBudget / len(writeCols) // 子批行数 = 预算 ÷ 列数（至少 1 行）
	if subRows < 1 {
		subRows = 1
	}
	var rowsDone int64
	scan := make([]any, len(srcCols))
	ptrs := make([]any, len(srcCols))
	for i := range scan {
		ptrs[i] = &scan[i]
	}
	var batchVals [][]any
	flush := func() error {
		if len(batchVals) == 0 {
			return nil
		}
		if err := writeSubBatches(ctx, tgt, table, insertStmt, conflictSuffix, writeCols, batchVals, subRows); err != nil {
			return err
		}
		rowsDone += int64(len(batchVals))
		batchVals = batchVals[:0]
		// 批边界：整任务取消在此生效（当前批事务已提交，已写入行保留——重跑幂等续接）。
		if st.isCancelled() {
			return errMigrateCancelled
		}
		st.mu.Lock()
		if t := st.find(table); t != nil {
			t.RowsDone = rowsDone
		}
		st.mu.Unlock()
		setProgress(&taskcenter.Progress{Text: st.summaryText(table), Detail: st.snapshot()})
		return nil
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			_ = flush()
			return rowsDone, fmt.Errorf("读取源表行失败: %w", err)
		}
		// 跨库编码边界净化：SQLite 动态类型宽容，源文本可能含历史遗留的非 UTF-8 字节
		// （如客户端 GBK 编码的原始请求路径），MySQL/PG 等严格校验的目标库会整批拒收。
		// 非法字节替换为 U+FFFD 替换符（不猜测源编码做转码，避免二次破坏）。
		for i := range scan {
			switch v := scan[i].(type) {
			case string:
				scan[i] = toValidUTF8(v)
			case []byte:
				scan[i] = []byte(toValidUTF8(string(v)))
			}
		}
		vals := make([]any, 0, len(writeCols))
		for i, c := range srcCols {
			for _, wc := range writeCols {
				if strings.EqualFold(c, wc) {
					vals = append(vals, scan[i])
					break
				}
			}
		}
		batchVals = append(batchVals, vals)
		if len(batchVals) >= p.batch {
			if err := flush(); err != nil {
				return rowsDone, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = flush()
		return rowsDone, fmt.Errorf("遍历源表失败: %w", err)
	}
	if err := flush(); err != nil {
		return rowsDone, err
	}

	// 自增序列重置（防换库后首批写入主键冲突；无自增主键的表内部自动跳过）。
	if err := resetAutoIncrement(ctx, tgt, table); err != nil {
		// 数据已迁完不回滚，但错误必须上抛留痕：不重置时换库后首批写入可能主键冲突。
		return rowsDone, fmt.Errorf("数据已迁完（%d 行）但自增序列重置失败: %w", rowsDone, err)
	}
	return rowsDone, nil
}

// writeSubBatches 把攒下的一批按占位符预算切子批写入（子批共享同一事务——批次=事务粒度不变）。
func writeSubBatches(ctx context.Context, tgt *db.DB, table, insertStmt, conflictSuffix string, cols []string, batchVals [][]any, subRows int) error {
	tx, err := tgt.EasyDB().GetSqlDB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // 提交后 Rollback 无效，仅错误路径回滚
	for start := 0; start < len(batchVals); start += subRows {
		end := start + subRows
		if end > len(batchVals) {
			end = len(batchVals)
		}
		part := batchVals[start:end]
		sqlText := insertStmt + multiValues(tgt.Driver(), len(part), len(cols)) + conflictSuffix
		args := make([]any, 0, len(part)*len(cols))
		for _, v := range part {
			args = append(args, v...)
		}
		if _, err := tx.ExecContext(ctx, sqlText, args...); err != nil {
			return fmt.Errorf("写入目标表 %s 失败（子批 %d 行）: %w", table, len(part), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

// quoteIdent 方言标识符引号（mysql 反引号，sqlite/pg 双引号）。
func quoteIdent(driver string) string {
	if driver == "mysql" {
		return "`"
	}
	return `"`
}

// buildInsertSQL 构造 INSERT 前缀（列名方言引号 + 冲突策略），VALUES 占位符段由 multiValues 生成，
// 冲突策略后缀由 insertConflictSuffix 生成（skip 的 ON CONFLICT 子句须位于 VALUES 之后，故拆两段）。
func buildInsertSQL(driver, table string, cols []string, mode string) string {
	q := quoteIdent(driver)
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = q + c + q
	}
	collist := strings.Join(parts, ", ")
	if mode == migrateModeSkip && driver == "mysql" {
		return "INSERT IGNORE INTO " + q + table + q + " (" + collist + ") VALUES "
	}
	return "INSERT INTO " + q + table + q + " (" + collist + ") VALUES "
}

// insertConflictSuffix 冲突策略后缀：skip 模式下 SQLite/PG 用 ON CONFLICT DO NOTHING 逐行跳过冲突
// （该子句语法上位于 VALUES 之后，MySQL 的同语义跳过用 INSERT IGNORE 前缀表达，见 buildInsertSQL）；
// replace 模式写前已清空目标表，无后缀。
func insertConflictSuffix(driver, mode string) string {
	if mode != migrateModeSkip || driver == "mysql" {
		return ""
	}
	return " ON CONFLICT DO NOTHING"
}

// multiValues 生成 nRows 行 × nCols 列的 VALUES 占位符段（如 (?,?),(?,?)）。
// 占位符方言差异：PostgreSQL（lib/pq）用编号占位符 $1..$N 且跨行连续编号，
// 其余方言（sqlite/mysql）统一用 ?。
func multiValues(driver string, nRows, nCols int) string {
	var b strings.Builder
	n := 0
	for r := 0; r < nRows; r++ {
		if r > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for c := 0; c < nCols; c++ {
			if c > 0 {
				b.WriteByte(',')
			}
			n++
			if driver == "postgres" {
				b.WriteByte('$')
				b.WriteString(strconv.Itoa(n))
				continue
			}
			b.WriteByte('?')
		}
		b.WriteByte(')')
	}
	return b.String()
}

// toValidUTF8 字符串净化：非法 UTF-8 字节序列替换为 U+FFFD（与 Go/PostgreSQL 惯例一致）。
func toValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// countSQL 各方言行数统计。
func countSQL(driver, table string) string {
	q := quoteIdent(driver)
	return "SELECT COUNT(*) FROM " + q + table + q
}

// selectAllSQL 各方言全表读（流式游标：逐行迭代攒批即写，无 OFFSET 翻页、不依赖主键形态。
// 注意 PG/MySQL 协议特性使服务端仍会物化/推送完整结果集，超大表需评估源库侧占用）。
func selectAllSQL(driver, table string) string {
	q := quoteIdent(driver)
	return "SELECT * FROM " + q + table + q
}

// truncateSQL 清空目标表：MySQL/PG TRUNCATE（更快），SQLite 用 DELETE（TRUNCATE 非其方言语法）。
func truncateSQL(driver, table string) string {
	q := quoteIdent(driver)
	if driver == "sqlite" {
		return "DELETE FROM " + q + table + q
	}
	return "TRUNCATE TABLE " + q + table + q
}
