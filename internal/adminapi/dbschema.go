// dbschema.go：数据库表结构同步端点（§docs/DB_SCHEMA_SYNC_PLAN.md）。
//
//	GET  /admin/db/schema —— 表结构检查：期望（运行期 SQLSource 脚本）vs 实际（catalog），
//	                        返回 A-F 分级差异与自动项生成的 SQL（直接喂前端编辑器）。
//	POST /admin/db/exec   —— 执行 SQL：拆句逐条执行、遇错即停（DDL 无跨方言统一事务语义，
//	                        返回已执行到的位置，失败文案指明可仅重发剩余语句）；
//	                        每条语句执行留痕落 sql_exec_log 表（审计）。
//	GET  /admin/db/execlog —— SQL 执行历史查询（时间倒序，服务端分页）。
//
// ★ 安全边界（决策 2）：执行端点为 danger 级强确认操作（前端 confirmDialog 兜底），
// 服务端不做语句类型白名单——编辑器内容可自由编辑（含手工救急语句），原样逐条执行。
package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/db"
	"rocksys/internal/netutil"
)

// 数据库表结构同步端点路径（§8.1 表）。
const (
	PathDBSchema  = "/admin/db/schema"
	PathDBExec    = "/admin/db/exec"
	PathDBExecLog = "/admin/db/execlog"
)

// SetTableSpecs 注入数据连接与表清单（装配处单一事实来源，cmd/rocksys/main.go 调用）。
// 表名无法从脚本文件名推断（如 mq_create_table.sql 实际表名 outbox），必须在装配处注册；
// dataDB 为统一数据访问层（catalog 查询与 SQL 执行都走它），两者任缺其一则表结构检查不可用。
func (s *AdminServer) SetTableSpecs(dataDB *db.DB, specs []db.TableSpec) {
	s.dataDB = dataDB
	s.tableSpecs = specs
}

// handleDBSchema 表结构检查：逐表比对期望与实际结构，返回差异项与自动项生成 SQL。
func (s *AdminServer) handleDBSchema(w http.ResponseWriter, r *http.Request) {
	if s.dataDB == nil || len(s.tableSpecs) == 0 {
		http.Error(w, "表结构检查不可用：数据连接或表清单未装配（请联系管理员检查 main.go 装配）", http.StatusServiceUnavailable)
		return
	}
	items, err := db.DiffSchema(r.Context(), s.dataDB, s.tableSpecs)
	if err != nil {
		http.Error(w, "表结构检查失败："+err.Error()+"；请确认数据连接正常后重试", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []db.DiffItem{}
	}
	sqlText := ""
	if len(items) > 0 {
		if sqlText, err = db.GenerateSQL(items, s.tableSpecs, s.dataDB); err != nil {
			http.Error(w, "生成同步 SQL 失败："+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	_ = writeJSON(w, map[string]any{
		"driver": s.dataDB.Driver(),
		"items":  items,
		"sql":    sqlText,
	}, http.StatusOK)
}

// dbExecMaxBody /admin/db/exec 请求体上限（DDL 文本远小于此，防误传大负载）。
const dbExecMaxBody = 1 << 20

// handleDBExec 执行 SQL：拆句逐条执行、遇错即停；返回逐条结果与已执行/失败计数。
// 进程内互斥：DDL 无法回滚，两个会话并发执行会交叉产生不可预期状态，后者直接拒绝。
func (s *AdminServer) handleDBExec(w http.ResponseWriter, r *http.Request) {
	if s.dataDB == nil {
		http.Error(w, "SQL 执行不可用：数据连接未装配", http.StatusServiceUnavailable)
		return
	}
	if !s.execMu.TryLock() {
		http.Error(w, "已有另一批 SQL 正在执行（DDL 不可并发交叉），请等待其完成后再试；可刷新表结构检查确认当前状态", http.StatusConflict)
		return
	}
	defer s.execMu.Unlock()
	var body struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, dbExecMaxBody)).Decode(&body); err != nil || strings.TrimSpace(body.SQL) == "" {
		http.Error(w, "请求体须为 {\"sql\": \"...\"} 且内容非空（输入框为空时「执行SQL」按钮应置灰）", http.StatusBadRequest)
		return
	}
	stmts := db.SplitStatements(body.SQL)
	if len(stmts) == 0 {
		http.Error(w, "未解析出可执行语句：内容仅含注释或空白，请检查输入", http.StatusBadRequest)
		return
	}
	// 审计埋点准备：批次标识 + 客户端 IP（落 sql_exec_log 表，每条语句一行）。
	batchID, err := newBatchID()
	if err != nil {
		batchID = fmt.Sprintf("manual-%d", time.Now().UnixNano()) // 随机源不可用时的兜底标识（保证批次可归组）
	}
	clientIP := netutil.GetClientIP(r)
	entries := make([]*ExecLogEntry, 0, len(stmts))

	results := make([]map[string]any, 0, len(stmts))
	executed, failed := 0, 0
	for i, stmt := range stmts {
		item := map[string]any{"sql": stmt, "ok": false}
		start := time.Now()
		res, err := s.dataDB.EasyDB().GetSqlDB().ExecContext(r.Context(), stmt)
		costMS := time.Since(start).Milliseconds()
		if err != nil {
			item["error"] = err.Error()
			results = append(results, item)
			failed++
			entries = append(entries, &ExecLogEntry{
				Time: time.Now().UTC(), BatchID: batchID, Seq: i + 1, SQLText: stmt,
				OK: false, Error: err.Error(), DurationMS: costMS,
				ClientIP: clientIP, Source: "webui",
			})
			s.recordExecLog(entries)
			// 文案三要素：发生了什么 + 为什么 + 下一步（前 N-1 条已生效、可仅重发剩余语句）。
			_ = writeJSON(w, map[string]any{
				"results":  results,
				"executed": executed,
				"failed":   failed,
				"message": fmt.Sprintf("第 %d 条执行失败：%s。前面 %d 条已生效且不可回滚；请修正该语句后仅重发剩余部分，再执行表结构检查复核",
					i+1, err.Error(), executed),
			}, http.StatusOK)
			return
		}
		rows := int64(0)
		if res != nil {
			if n, rerr := res.RowsAffected(); rerr == nil {
				rows = n
				item["rows"] = n
			}
		}
		item["ok"] = true
		results = append(results, item)
		executed++
		entries = append(entries, &ExecLogEntry{
			Time: time.Now().UTC(), BatchID: batchID, Seq: i + 1, SQLText: stmt,
			OK: true, RowsAffected: rows, DurationMS: costMS,
			ClientIP: clientIP, Source: "webui",
		})
	}
	s.recordExecLog(entries)
	_ = writeJSON(w, map[string]any{
		"results":  results,
		"executed": executed,
		"failed":   failed,
	}, http.StatusOK)
}

// execLogStore 惰性构造 SQL 执行审计存储（dataDB 未装配时返回 nil——审计不可用，
// 执行端点跳过埋点但不影响执行本身）。
func (s *AdminServer) execLogStoreLazy() *execLogStore {
	if s.dataDB == nil {
		return nil
	}
	s.execLogOnce.Do(func() {
		s.execLog = newExecLogStore(s.dataDB.EasyDB(), s.dataDB)
	})
	return s.execLog
}

// recordExecLog 落一批审计记录：审计失败只记告警日志，不影响执行结果返回
// （DDL 已生效，回滚无意义；告警提示管理员核对表状态）。
func (s *AdminServer) recordExecLog(entries []*ExecLogEntry) {
	if len(entries) == 0 {
		return
	}
	store := s.execLogStoreLazy()
	if store == nil {
		return
	}
	if err := store.Insert(entries); err != nil {
		log.Warn("adminapi: SQL 执行审计落库失败（执行本身已生效，请人工核对表结构）", "batch", entries[0].BatchID, "err", err)
	}
}

// handleDBExecLog SQL 执行历史查询：时间倒序 + offset 服务端分页。
func (s *AdminServer) handleDBExecLog(w http.ResponseWriter, r *http.Request) {
	store := s.execLogStoreLazy()
	if store == nil {
		http.Error(w, "SQL 执行历史不可用：数据连接未装配", http.StatusServiceUnavailable)
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	items, err := store.Query(limit, offset)
	if err != nil {
		http.Error(w, "SQL 执行历史查询失败："+err.Error()+"；请确认数据连接正常后重试", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []*ExecLogRecord{}
	}
	total, err := store.Count()
	if err != nil {
		http.Error(w, "SQL 执行历史计数失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = writeJSON(w, map[string]any{"items": items, "total": total}, http.StatusOK)
}

// parseIntDefault 解析非负整数查询参数（空/非法返回默认值）。
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return def
		}
	}
	return n
}
