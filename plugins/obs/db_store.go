// DBStore 访问日志数据库存储后端（OBS_STORE=db）：
// 复用统一数据访问层 internal/db（DB_DRIVER/DB_DSN，默认 sqlite rocksys.db），
// SQL 全部外置 sql/<dbtype>/（外置目录优先、嵌入兜底，遵循项目铁律）。
// 表结构：14 个索引列（维度化固定列）+ extra JSON 列（负载维度），见 dim.go。
package obs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iotames/easydb"

	"rocksys/internal/db"
)

// accessLogTable 访问日志表名（数据访问层内，与 sql 脚本 {table} 占位符对应）。
const accessLogTable = "access_log"

// DBStore 数据库存储后端。
type DBStore struct {
	edb  *easydb.EasyDb
	sqls db.SQLSource // SQL 脚本源（internal/db 数据访问层）
	tableName string
}

// NewDBStore 基于统一数据访问层构造数据库存储后端。
func NewDBStore(d *db.DB, tableName string) *DBStore {
	if tableName == "" {
		tableName = accessLogTable
	}
	return &DBStore{edb: d.EasyDB(), sqls: d, tableName: tableName}
}

// Name 后端名。
func (s *DBStore) Name() string { return "db" }

// sqlText 读取脚本并替换 {table} 表名占位符。
func (s *DBStore) sqlText(name string) (string, error) {
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("obs: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	return strings.ReplaceAll(txt, "{table}", s.tableName), nil
}

// EnsureTable 幂等建表 + 索引。
func (s *DBStore) EnsureTable() error {
	ddl, err := s.sqlText("access_log_create_table.sql")
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(ddl); err != nil {
		return fmt.Errorf("obs: 建访问日志表失败: %w", err)
	}
	idx, err := s.sqlText("access_log_create_index.sql")
	if err != nil {
		return err
	}
	// 索引脚本含多条语句，逐条执行；"已存在"类错误幂等忽略
	// （MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS）。
	for _, stmt := range strings.Split(idx, "\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.edb.Exec(stmt); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
				continue
			}
			return fmt.Errorf("obs: 建访问日志索引失败: %w", err)
		}
	}
	return nil
}

// Write 同步逐条插入一批记录。
func (s *DBStore) Write(batch []*AccessRecord) error {
	ins, err := s.sqlText("access_log_insert.sql")
	if err != nil {
		return err
	}
	for _, r := range batch {
		extra, err := r.extrasJSON()
		if err != nil {
			continue // 单条扩展字段序列化失败单独丢弃
		}
		if _, err := s.edb.Exec(ins,
			r.Time.Format(time.RFC3339),
			r.TraceID, r.TenantID, r.Path, r.Method, r.ClientIP, r.StatusCode,
			r.Upstream, r.ShieldMs, r.BizMs, r.TotalMs, r.ReqBytes, r.RespBytes,
			extra,
		); err != nil {
			return fmt.Errorf("obs: 插入访问日志失败: %w", err)
		}
	}
	return nil
}

// Query 按条件查询，返回平铺维度 map（extra 已合并进顶层）。
func (s *DBStore) Query(q Query) ([]map[string]any, error) {
	sel, err := s.sqlText("access_log_query.sql")
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	args := []any{
		q.From.Format(time.RFC3339), q.To.Format(time.RFC3339),
		q.Path, q.Path, q.PathLike, q.PathLike, q.TraceID, q.TraceID,
		limit,
	}
	var rows []map[string]any
	if err := s.edb.GetMany(sel, &rows, args...); err != nil {
		return nil, fmt.Errorf("obs: 查询访问日志失败: %w", err)
	}
	for _, row := range rows {
		mergeExtras(row)
	}
	return rows, nil
}

// SizeBytes 返回访问日志表 + 索引的总字节数（sql/mysql/postgres 三方言各一脚本）。
func (s *DBStore) SizeBytes() (int64, error) {
	sel, err := s.sqlText("access_log_size.sql")
	if err != nil {
		return 0, err
	}
	data := make(map[string]any)
	if err := s.edb.GetOneData(sel, data); err != nil {
		return 0, fmt.Errorf("obs: 查询访问日志表大小失败: %w", err)
	}
	for _, v := range data {
		return toInt64(v), nil
	}
	return 0, nil
}

// toInt64 将数据库标量（int64/float64/string/[]byte）归一为 int64。
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
	case []byte:
		return toInt64(string(n))
	}
	return 0
}

// Flush 无缓冲（连接层自动提交），直接返回。
func (s *DBStore) Flush(ctx context.Context) error { return nil }

// Close 连接由统一数据访问层（main 停机时 Close）统一管理，no-op。
func (s *DBStore) Close() error { return nil }
