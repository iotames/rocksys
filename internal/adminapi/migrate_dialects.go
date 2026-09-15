// migrate_dialects.go：迁移执行器的三方言目录辅助与表级状态端点。
//
// 目标列/自增主键的探测按方言分写（catalog 查询无跨方言统一入口）；
// 自增序列重置仅对含整型自增主键的表生效，探测不到即静默跳过（如文本主键表）。
package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"rocksys/internal/db"
)

// columnInfo 目标表单列信息。
type columnInfo struct {
	Name string
	PK   bool // 主键列
	Auto bool // 自增列（sqlite: INTEGER PRIMARY KEY 即 ROWID 别名；mysql: extra=auto_increment；pg: default=nextval）
}

// targetColumns 读目标表列清单（方言分写）。
func targetColumns(ctx context.Context, d *db.DB, table string) ([]string, error) {
	infos, err := targetColumnInfos(ctx, d, table)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(infos))
	for _, c := range infos {
		out = append(out, c.Name)
	}
	return out, nil
}

// targetColumnInfos 读目标表列信息（含主键/自增标记）。
func targetColumnInfos(ctx context.Context, d *db.DB, table string) ([]columnInfo, error) {
	sqldb := d.EasyDB().GetSqlDB()
	switch d.Driver() {
	case "sqlite":
		rows, err := sqldb.QueryContext(ctx, "PRAGMA table_info(\""+table+"\")")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				return nil, err
			}
			// sqlite 的自增语义 = INTEGER PRIMARY KEY（ROWID 别名，整型单列主键）。
			out = append(out, columnInfo{Name: name, PK: pk > 0,
				Auto: pk > 0 && strings.Contains(strings.ToUpper(ctype), "INT")})
		}
		return out, rows.Err()
	case "mysql":
		rows, err := sqldb.QueryContext(ctx,
			"SELECT column_name, column_key, extra FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ?", table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var name, key, extra string
			if err := rows.Scan(&name, &key, &extra); err != nil {
				return nil, err
			}
			out = append(out, columnInfo{Name: name, PK: key == "PRI",
				Auto: strings.Contains(extra, "auto_increment")})
		}
		return out, rows.Err()
	case "postgres":
		rows, err := sqldb.QueryContext(ctx, `SELECT c.column_name,
			       COALESCE(tc.constraint_type = 'PRIMARY KEY', false) AS is_pk,
			       COALESCE(c.column_default LIKE 'nextval%', false) AS is_auto
			FROM information_schema.columns c
			LEFT JOIN information_schema.key_column_usage kcu
			       ON kcu.table_name = c.table_name AND kcu.column_name = c.column_name
			LEFT JOIN information_schema.table_constraints tc
			       ON tc.constraint_name = kcu.constraint_name AND tc.constraint_type = 'PRIMARY KEY'
			WHERE c.table_name = $1`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var name string
			var pk, auto bool
			if err := rows.Scan(&name, &pk, &auto); err != nil {
				return nil, err
			}
			out = append(out, columnInfo{Name: name, PK: pk, Auto: auto})
		}
		return out, rows.Err()
	default:
		return nil, fmt.Errorf("不支持的数据库方言 %s", d.Driver())
	}
}

// resetAutoIncrement 目标表自增序列重置为当前 max(pk)：换库后首批写入主键 = max+1 不冲突。
// 探测不到自增主键列时跳过（nil, nil）。调用时机 = 该表数据迁完后。
func resetAutoIncrement(ctx context.Context, d *db.DB, table string) error {
	infos, err := targetColumnInfos(ctx, d, table)
	if err != nil {
		return err
	}
	pkCol := ""
	for _, c := range infos {
		if c.Auto {
			pkCol = c.Name
			break
		}
	}
	if pkCol == "" {
		return nil // 无自增主键（如文本主键表），自动跳过
	}
	sqldb := d.EasyDB().GetSqlDB()
	q := quoteIdent(d.Driver())
	switch d.Driver() {
	case "sqlite":
		// sqlite_sequence 行在首次自增插入后才存在：先 UPDATE，无行则 INSERT。
		res, err := sqldb.ExecContext(ctx,
			"UPDATE sqlite_sequence SET seq = (SELECT COALESCE(MAX("+q+pkCol+q+"), 0) FROM "+q+table+q+") WHERE name = ?", table)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_, err := sqldb.ExecContext(ctx,
				"INSERT INTO sqlite_sequence (name, seq) SELECT ?, COALESCE(MAX("+q+pkCol+q+"), 0) FROM "+q+table+q, table)
			return err
		}
		return nil
	case "mysql":
		var maxVal any
		if err := sqldb.QueryRowContext(ctx, "SELECT MAX("+q+pkCol+q+") FROM "+q+table+q).Scan(&maxVal); err != nil {
			return err
		}
		if maxVal == nil {
			return nil // 空表无需重置
		}
		_, err := sqldb.ExecContext(ctx, "ALTER TABLE "+q+table+q+" AUTO_INCREMENT = ?", maxVal)
		return err
	case "postgres":
		var seqName string
		if err := sqldb.QueryRowContext(ctx, "SELECT pg_get_serial_sequence($1, $2)", table, pkCol).Scan(&seqName); err != nil {
			return err
		}
		if seqName == "" {
			return nil // 无序列绑定（非 serial/identity 列），跳过
		}
		_, err := sqldb.ExecContext(ctx,
			"SELECT setval($1, COALESCE((SELECT MAX("+q+pkCol+q+") FROM "+q+table+q+"), 0) + 1, false)", seqName)
		return err
	default:
		return fmt.Errorf("不支持的数据库方言 %s", d.Driver())
	}
}

// handleMigrateStatus 迁移任务状态：表级明细便捷视图（最近一次迁移任务的进度快照）。
// 任务全局元数据（Status/Result/耗时）经 /admin/tasks/{id} 查询，此处只回表级明细。
func (s *AdminServer) handleMigrateStatus(w http.ResponseWriter, r *http.Request) {
	s.migMu.Lock()
	st := s.migState
	s.migMu.Unlock()
	if st == nil {
		_ = writeJSON(w, map[string]any{"state": "idle", "tables": []tableProgress{}}, http.StatusOK)
		return
	}
	taskID := ""
	st.mu.Lock()
	taskID = st.taskID
	st.mu.Unlock()
	resp := map[string]any{
		"state":  "running/finished", // 精确终态以任务中心为准
		"tables": st.snapshot(),
	}
	if taskID != "" {
		resp["task_id"] = taskID
		if task, ok := s.tasks.Get(taskID); ok {
			resp["state"] = string(task.Status)
			resp["result"] = task.Result
		}
	}
	_ = writeJSON(w, resp, http.StatusOK)
}

// handleMigrateCancel 取消：带 table= 取消该待迁移表（仅 pending 可取消，从本次任务移除）；
// 不带 = 取消整个任务（当前批事务完成后停止，已写入行保留——重跑幂等续接）。
func (s *AdminServer) handleMigrateCancel(w http.ResponseWriter, r *http.Request) {
	s.migMu.Lock()
	st := s.migState
	s.migMu.Unlock()
	if st == nil {
		http.Error(w, "当前没有迁移任务可取消（状态页可见最近任务进度）", http.StatusConflict)
		return
	}
	var body struct {
		Table string `json:"table"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // 空请求体 = 整任务取消
	if body.Table != "" {
		if st.cancelTable(body.Table) {
			_ = writeJSON(w, map[string]any{"ok": true, "message": "表 " + body.Table + " 已从本次任务移除（仅待迁移表可取消）"}, http.StatusOK)
			return
		}
		http.Error(w, "表 "+body.Table+" 不在待迁移状态（仅 pending 表可单表取消；迁移中/已完成表不可取消）", http.StatusConflict)
		return
	}
	// 整任务取消：置标记（批边界生效）+ 任务中心取消信号（context 送达）。
	st.mu.Lock()
	st.cancelled = true
	taskID := st.taskID
	st.mu.Unlock()
	msg := "取消请求已送达：当前批次事务完成后停止，剩余表将置为已取消；已写入数据保留（重跑幂等续接）"
	if taskID != "" {
		if task, hint, ok := s.tasks.Cancel(taskID); ok {
			msg = msg + "；任务中心：" + hint
			_ = task
		}
	}
	_ = writeJSON(w, map[string]any{"ok": true, "message": msg}, http.StatusOK)
}
