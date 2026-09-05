// execlogstore.go：SQL 执行审计存储。
// 记录管理端「数据库同步 / 执行SQL」的每条语句执行留痕（谁在何时执行了什么、成败与耗时），
// 供「服务 → 数据库」页执行历史区块查询展示。表结构见 sql/<dbtype>/sql_exec_log_*.sql。
package adminapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iotames/easydb"

	"rocksys/internal/db"
)

// sqlExecLogTable SQL 执行审计表名（与 sql 脚本 {table} 占位符对应）。
const sqlExecLogTable = "sql_exec_log"

// ExecLogEntry 单条 SQL 执行审计记录（写入侧）。
type ExecLogEntry struct {
	Time         time.Time // 语句执行完成时刻（UTC）
	BatchID      string    // 批次标识：同一次「执行SQL」提交归一组
	Seq          int       // 批内序号（从 1 起）
	SQLText      string    // SQL 语句原文（单条）
	OK           bool      // 执行结果
	RowsAffected int64     // 受影响行数
	Error        string    // 失败原因（成功为空串）
	DurationMS   int64     // 单条语句执行耗时（毫秒）
	ClientIP     string    // 发起执行的客户端 IP
	Source       string    // 触发来源（webui/api/…）
}

// execLogStore SQL 执行审计存储：复用统一数据访问层（dataDB），
// SQL 全部外置 sql/<dbtype>/（外置目录优先、嵌入兜底，遵循项目铁律）。
type execLogStore struct {
	edb       *easydb.EasyDb
	sqls      db.SQLSource
	tableName string

	ensureOnce sync.Once // 建表只需成功执行一次（幂等 DDL），后续调用直接复用结果
	ensureErr  error
}

// newExecLogStore 构造 SQL 执行审计存储（连接与脚本源缺失时返回 nil——审计降级为不可用，
// 由调用方在执行埋点处跳过，不影响 SQL 执行本身）。
func newExecLogStore(edb *easydb.EasyDb, sqls db.SQLSource) *execLogStore {
	if edb == nil || sqls == nil {
		return nil
	}
	return &execLogStore{edb: edb, sqls: sqls, tableName: sqlExecLogTable}
}

// sqlText 读取脚本并替换 {table} 表名占位符。
func (s *execLogStore) sqlText(name string) (string, error) {
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("adminapi: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	return strings.ReplaceAll(txt, "{table}", s.tableName), nil
}

// ensure 幂等建表 + 索引（仅首次调用实际执行，之后复用结果）。
func (s *execLogStore) ensure() error {
	s.ensureOnce.Do(func() {
		ddl, err := s.sqlText("sql_exec_log_create_table.sql")
		if err != nil {
			s.ensureErr = err
			return
		}
		if _, err := s.edb.Exec(ddl); err != nil {
			s.ensureErr = fmt.Errorf("adminapi: 建 SQL 执行审计表失败: %w", err)
			return
		}
		idx, err := s.sqlText("sql_exec_log_create_index.sql")
		if err != nil {
			s.ensureErr = err
			return
		}
		// 多语句脚本逐条执行 + 幂等容错："已存在"类错误忽略
		// （MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报 "Duplicate key name"）。
		for _, stmt := range db.SplitSQLStatements(idx) {
			if _, err := s.edb.Exec(stmt); err != nil {
				msg := err.Error()
				if strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
					continue
				}
				s.ensureErr = fmt.Errorf("adminapi: 建 SQL 执行审计索引失败: %w", err)
				return
			}
		}
	})
	return s.ensureErr
}

// Insert 同步落一批审计记录（一次「执行SQL」提交的多条语句）。
// 审计失败不阻断执行结果返回，由调用方记日志告警。
func (s *execLogStore) Insert(entries []*ExecLogEntry) error {
	if err := s.ensure(); err != nil {
		return err
	}
	ins, err := s.sqlText("sql_exec_log_insert.sql")
	if err != nil {
		return err
	}
	for _, e := range entries {
		ok := 0
		if e.OK {
			ok = 1
		}
		if _, err := s.edb.Exec(ins,
			e.Time.UTC(), e.BatchID, e.Seq, e.SQLText, ok,
			e.RowsAffected, e.Error, e.DurationMS, e.ClientIP, e.Source,
		); err != nil {
			return fmt.Errorf("adminapi: 插入 SQL 执行审计失败: %w", err)
		}
	}
	return nil
}

// ExecLogRecord 查询侧单条审计记录（已按 JSON 输出格式归一）。
type ExecLogRecord struct {
	ID           int64  `json:"id"`
	Time         string `json:"time"` // RFC3339（UTC）
	BatchID      string `json:"batch_id"`
	Seq          int    `json:"seq"`
	SQLText      string `json:"sql_text"`
	OK           bool   `json:"ok"`
	RowsAffected int64  `json:"rows_affected"`
	Error        string `json:"error"`
	DurationMS   int64  `json:"duration_ms"`
	ClientIP     string `json:"client_ip"`
	Source       string `json:"source"`
}

// Query 按时间倒序分页查询审计记录。
func (s *execLogStore) Query(limit, offset int) ([]*ExecLogRecord, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	sel, err := s.sqlText("sql_exec_log_query.sql")
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := s.edb.GetMany(sel, &rows, limit, offset); err != nil {
		return nil, fmt.Errorf("adminapi: 查询 SQL 执行审计失败: %w", err)
	}
	out := make([]*ExecLogRecord, 0, len(rows))
	for _, row := range rows {
		r := &ExecLogRecord{}
		r.ID = mapInt64(row, "id")
		r.Time = mapTime(row, "time")
		r.BatchID, _ = row["batch_id"].(string)
		r.Seq = int(mapInt64(row, "seq"))
		r.SQLText, _ = row["sql_text"].(string)
		r.OK = mapInt64(row, "ok") == 1
		r.RowsAffected = mapInt64(row, "rows_affected")
		if v, ok := row["error"].(string); ok {
			r.Error = v
		}
		r.DurationMS = mapInt64(row, "duration_ms")
		if v, ok := row["client_ip"].(string); ok {
			r.ClientIP = v
		}
		if v, ok := row["source"].(string); ok {
			r.Source = v
		}
		out = append(out, r)
	}
	return out, nil
}

// Count 审计记录总数（服务端分页用）。
func (s *execLogStore) Count() (int64, error) {
	if err := s.ensure(); err != nil {
		return 0, err
	}
	cnt, err := s.sqlText("sql_exec_log_count.sql")
	if err != nil {
		return 0, err
	}
	var rows []map[string]any
	if err := s.edb.GetMany(cnt, &rows); err != nil {
		return 0, fmt.Errorf("adminapi: 统计 SQL 执行审计总数失败: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return mapInt64(rows[0], "cnt"), nil
}

// mapInt64 从查询行取整型值（各方言驱动返回类型不一，统一归一）。
func mapInt64(row map[string]any, key string) int64 {
	switch v := row[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		var n int64
		_, _ = fmt.Sscanf(string(v), "%d", &n)
		return n
	}
	return 0
}

// mapTime 时间列归一为 RFC3339 字符串（驱动可能返回 time.Time 或字符串）。
func mapTime(row map[string]any, key string) string {
	switch v := row[key].(type) {
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

// newBatchID 生成批次标识（16 字节随机 → hex 32 字符，与 trace_id 同源强度）。
func newBatchID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
