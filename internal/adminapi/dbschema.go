// dbschema.go：数据库表结构同步端点（§docs/DB_SCHEMA_SYNC_PLAN.md）。
//
//	GET  /admin/db/schema —— 表结构检查：期望（运行期 SQLSource 脚本）vs 实际（catalog），
//	                        返回 A-F 分级差异与自动项生成的 SQL（直接喂前端编辑器）。
//	POST /admin/db/exec   —— 执行 SQL：拆句逐条执行、遇错即停（DDL 无跨方言统一事务语义，
//	                        返回已执行到的位置，失败文案指明可仅重发剩余语句）。
//
// ★ 安全边界（决策 2）：执行端点为 danger 级强确认操作（前端 confirmDialog 兜底），
// 服务端不做语句类型白名单——编辑器内容可自由编辑（含手工救急语句），原样逐条执行。
package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"rocksys/internal/db"
)

// 数据库表结构同步端点路径（§8.1 表）。
const (
	PathDBSchema = "/admin/db/schema"
	PathDBExec   = "/admin/db/exec"
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
	items, err := db.DiffSchema(s.dataDB, s.tableSpecs)
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
	results := make([]map[string]any, 0, len(stmts))
	executed, failed := 0, 0
	for i, stmt := range stmts {
		item := map[string]any{"sql": stmt, "ok": false}
		res, err := s.dataDB.EasyDB().GetSqlDB().ExecContext(r.Context(), stmt)
		if err != nil {
			item["error"] = err.Error()
			results = append(results, item)
			failed++
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
		if res != nil {
			if n, rerr := res.RowsAffected(); rerr == nil {
				item["rows"] = n
			}
		}
		item["ok"] = true
		results = append(results, item)
		executed++
	}
	_ = writeJSON(w, map[string]any{
		"results":  results,
		"executed": executed,
		"failed":   failed,
	}, http.StatusOK)
}
