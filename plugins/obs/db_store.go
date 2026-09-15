// DBStore 访问日志数据库存储后端：
// 复用统一数据访问层 internal/db（DB_DRIVER/DB_DSN，默认 sqlite rocksys.db），
// SQL 全部外置 sql/<dbtype>/（外置目录优先、嵌入兜底，遵循项目铁律）。
// 表结构：18 个索引列（维度化固定列）+ extra JSON 列（负载维度），见 dim.go。
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

// accessLogTable 访问日志表名（引用数据访问层唯一权威常量，与 sql 脚本 {table} 占位符对应）。
const accessLogTable = db.TableAccessLog

// DBStore 数据库存储后端。
type DBStore struct {
	edb       *easydb.EasyDb
	sqls      db.SQLSource // SQL 脚本源（internal/db 数据访问层）
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

// sqlText 读取脚本并替换 {table} 表名占位符与 {geo}(geoip_list) 关联表占位符。
func (s *DBStore) sqlText(name string) (string, error) {
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("obs: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	txt = strings.ReplaceAll(txt, "{table}", s.tableName)
	return strings.ReplaceAll(txt, "{geo}", db.TableGeoipList), nil
}

// EnsureTable 幂等建表 + 索引。access_log 明细查询 LEFT JOIN geoip_list（GEOIP_LIST 方案），
// 故随本表一并幂等确保关联表存在（新库未走 schema 同步时查询不因缺表失败）。
func (s *DBStore) EnsureTable() error {
	ddl, err := s.sqlText("access_log_create_table.sql")
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(ddl); err != nil {
		return fmt.Errorf("obs: 建访问日志表失败: %w", err)
	}
	if geo := s.geoDDL(); geo != "" {
		if _, err := s.edb.Exec(geo); err != nil {
			return fmt.Errorf("obs: 建 geoip_list 关联表失败: %w", err)
		}
		idx, err := s.geoIndexDDL()
		if err != nil {
			return err
		}
		if idx != "" {
			// 索引幂等容错（MySQL 无 IF NOT EXISTS，重复执行报 Duplicate key name 忽略）
			for _, stmt := range db.SplitSQLStatements(idx) {
				if _, err := s.edb.Exec(stmt); err != nil {
					if msg := err.Error(); strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
						continue
					}
					return fmt.Errorf("obs: 建 geoip_list 索引失败: %w", err)
				}
			}
		}
	}
	idx, err := s.sqlText("access_log_create_index.sql")
	if err != nil {
		return err
	}
	// 多语句脚本逐条执行 + 幂等容错："已存在"类错误忽略
	// （MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报 "Duplicate key name"）。
	for _, stmt := range db.SplitSQLStatements(idx) {
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

// geoDDL 读 geoip_list 建表脚本并替换 {table} 占位符（脚本缺失返回空串由建表流程显式告警）。
func (s *DBStore) geoDDL() string {
	txt, err := s.sqls.SQL("geoip_list_create_table.sql")
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(txt, "{table}", db.TableGeoipList)
}

// geoIndexDDL 读 geoip_list 索引脚本并替换 {table} 占位符。
// 与 geoDDL 的容错跳过不同：索引脚本三方言内嵌必有（外挂缺失走内嵌兜底），读取失败即真异常，显式报错。
func (s *DBStore) geoIndexDDL() (string, error) {
	txt, err := s.sqls.SQL("geoip_list_create_index.sql")
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(txt, "{table}", db.TableGeoipList), nil
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
			r.Time.UTC(),
			r.TraceID, r.TenantID, r.Path, r.Method, r.ClientIP, r.StatusCode,
			r.Upstream, r.ShieldMs, r.BizMs, r.TotalMs, r.EgressMs, r.ReqBytes, r.RespBytes,
			r.UserAgent, extra,
		); err != nil {
			return fmt.Errorf("obs: 插入访问日志失败: %w", err)
		}
	}
	return nil
}

// statusBounds 将状态过滤合成闭区间 [lo, hi]（SQL 侧 status_code BETWEEN，可走状态索引）：
// 不过滤 → 0..999999；status_group '2'-'5' → 200..299 等；仅异常 → lo 提到 400。
// 契约仅支持 '2'-'5'（webui-api §3.20），其余取值（含 '1'/'9'/非法串）一律视为不过滤（全量区间）。
// 两者同时生效取交集（下界取更严者、上界保持分组界，可能为空集，与旧 AND 语义一致）。
func statusBounds(q Query) (lo, hi int) {
	lo, hi = 0, 999999
	if g := q.StatusGroup; g >= "2" && g <= "5" {
		if n := int(g[0]-'0') * 100; n > lo {
			lo = n
		}
		hi = lo + 99
	}
	if q.OnlyError && lo < 400 {
		lo = 400
	}
	return lo, hi
}

// statusFilterArgs 前置过滤参数（两查询脚本与计数脚本共用，保证 WHERE 条件口径一致）。
func statusFilterArgs(q Query) []any {
	lo, hi := statusBounds(q)
	return []any{q.Path, q.Path, q.PathLike, q.PathLike, q.TraceID, q.TraceID, lo, hi}
}

// Query 按条件查询，返回平铺维度 map（extra 已合并进顶层）。
// 缺省排序（time_desc）走 access_log_query_time.sql（主键倒序免临时排序，高频路径）；
// 耗时排序走 access_log_query.sql（CASE 排序需物化，低频可接受）。
// 状态分组/仅异常已合成区间条件（见 statusBounds），offset 服务端分页。
func (s *DBStore) Query(q Query) ([]map[string]any, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	var script string
	var args []any
	if q.sortCode() == 0 {
		script = "access_log_query_time.sql"
		args = append([]any{q.From.UTC(), q.To.UTC()}, statusFilterArgs(q)...)
		args = append(args, limit, offset)
	} else {
		script = "access_log_query.sql"
		args = append([]any{q.From.UTC(), q.To.UTC()}, statusFilterArgs(q)...)
		args = append(args, q.sortCode(), q.sortCode(), limit, offset)
	}
	sel, err := s.sqlText(script)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := s.edb.GetMany(sel, &rows, args...); err != nil {
		return nil, fmt.Errorf("obs: 查询访问日志失败: %w", err)
	}
	for _, row := range rows {
		mergeExtras(row)
		normalizeRowTypes(row)
	}
	return rows, nil
}

// Count 按相同过滤条件（不含 limit/offset/sort）统计总数（服务端分页 X-Total-Count 用）。
func (s *DBStore) Count(q Query) (int64, error) {
	cnt, err := s.sqlText("access_log_count.sql")
	if err != nil {
		return 0, err
	}
	args := append([]any{q.From.UTC(), q.To.UTC()}, statusFilterArgs(q)...)
	var rows []map[string]any
	if err := s.edb.GetMany(cnt, &rows, args...); err != nil {
		return 0, fmt.Errorf("obs: 统计访问日志总数失败: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return toInt64(rows[0]["cnt"]), nil
}

// normalizeRowTypes 将 DB 查询返回的行按维度注册表（dimIndex）归一化类型：
// DimInt → int64；DimString/DimDatetime → string。
// 修复底层 easydb.decodeAny 把"纯数字字符串列"（如 trace_id="123"）误转成数值，
// 导致 map 查询结果类型漂移的问题；未注册的 key（extra 平铺的未知负载维度）保持原样。
func normalizeRowTypes(row map[string]any) {
	for k, v := range row {
		spec, ok := dimIndex[k]
		if !ok {
			// JOIN 关联表列（geoip_list 的 country_code/province/city 等）不在维度注册表，
			// 统一 []byte→string 兜底，防驱动返回 []byte 在 JSON 输出中变 base64。
			if b, isB := v.([]byte); isB {
				row[k] = string(b)
			}
			continue
		}
		switch spec.Type {
		case DimInt:
			row[k] = toInt64(v)
		case DimString, DimDatetime:
			row[k] = toString(v)
		}
	}
}

// toString 将数据库标量（[]byte/string/int64/float64/bool 等）归一为 string。
func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case time.Time:
		return s.UTC().Format(time.RFC3339)
	case int64:
		return strconv.FormatInt(s, 10)
	case int:
		return strconv.Itoa(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	default:
		return fmt.Sprint(v)
	}
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

// Prune 清理保留期外的访问日志，返回删除行数（幂等可重复执行）。
// retentionDays <= 0 时回落默认保留 7 天。参数为截止时刻 time.Time（DB 原生时间类型列）。
func (s *DBStore) Prune(retentionDays int) (int64, error) {
	del, err := s.sqlText("access_log_prune.sql")
	if err != nil {
		return 0, err
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	res, err := s.edb.Exec(del, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("obs: 清理访问日志失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
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
