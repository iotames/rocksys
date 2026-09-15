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
	"fmt"
	"net/http"

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

// migrateTableSpecs 期望结构表清单（与运行库表同步同源，保证迁移双方结构同名同义）。
func (s *AdminServer) migrateTableSpecs() []db.TableSpec { return s.tableSpecs }

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
	// 提交前预读差异：空 diff 直接返回免任务；非空把 DDL 带进任务（避免任务内再依赖请求上下文）。
	target, err := s.openTargetDB(code)
	if err != nil {
		http.Error(w, "打开目标库失败："+err.Error(), http.StatusBadRequest)
		return
	}
	defer target.Close()
	items, err := db.DiffSchema(r.Context(), target, s.tableSpecs)
	if err != nil {
		http.Error(w, "目标库结构检查失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(items) == 0 {
		_ = writeJSON(w, map[string]any{"ok": true, "noop": true, "message": "目标库结构与期望一致，无需对齐"}, http.StatusOK)
		return
	}
	sqlText, err := db.GenerateSQL(items, s.tableSpecs, target)
	if err != nil {
		http.Error(w, "生成对齐 SQL 失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	stmts := db.SplitStatements(sqlText)
	_ = target.Close()

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
	_ = writeJSON(w, map[string]any{"ok": true, "task_id": taskID, "total": len(stmts)}, http.StatusOK)
}

// runSchemaApply 逐条执行对齐 DDL：遇错即停（DDL 无跨方言统一事务语义，前序已生效不可回滚），
// 结果归入任务进度（逐条明细）与 Result（摘要）。执行期间应用层重开目标库连接（任务生命周期
// 与 HTTP 请求解耦，不能复用请求作用域连接）。
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

// migrateRequest 数据迁移启动请求（批次/冲突策略语义见 handleMigrateStart，数据迁移步骤实现）。
type migrateRequest struct {
	Source string   `json:"source"` // "self" = 本机运行库；否则为数据源 Code
	Target string   `json:"target"` // 数据源 Code（不允许指向运行库）
	Tables []string `json:"tables"` // 迁移表清单（期望结构业务表的子集，非空）
	Batch  int      `json:"batch"`  // 每批行数（默认 1000，服务端 clamp 100–10000；会话内存态不落盘）
	Mode   string   `json:"mode"`   // replace = 清空重灌（默认）；skip = 跳过冲突行
}
