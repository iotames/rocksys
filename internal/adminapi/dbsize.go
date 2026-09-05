// dbsize.go：数据库空间占用端点。
//
//	GET /admin/db/size —— 库内全部基础表的占用统计：表名/备注/数据条数/占用空间，
//	                     及总占用空间。供「服务 → 数据库」页公共状态区与「数据表概览」页签。
//
// 口径说明：
//   - 条数为精确值：表清单/备注/空间由方言系统表查询，条数对每张表动态执行 COUNT(*)（MySQL
//     information_schema.TABLE_ROWS、PG n_live_tup 均为估算值，不满足运营精确口径）；
//   - 占用空间为方言系统表给出的数据+索引合计（MySQL DATA_LENGTH+INDEX_LENGTH、
//     PG pg_total_relation_size；SQLite 无系统表，走 dbstat 按页聚合，dbstat 不可用时逐表置 0）；
//   - SQLite 总空间取 page_count×page_size（库级口径，含空闲页），与逐表 SUM 可能不一致。
package adminapi

import (
	"net/http"
	"sort"

	"github.com/iotames/easydb"
)

// TableStat 单张表的空间占用统计。
type TableStat struct {
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Rows    int64  `json:"rows"`
	Bytes   int64  `json:"bytes"`
}

// handleDBSize 库内全部基础表占用统计（只读，不落库）。
func (s *AdminServer) handleDBSize(w http.ResponseWriter, r *http.Request) {
	if s.dataDB == nil {
		http.Error(w, "空间统计不可用：数据连接未装配", http.StatusServiceUnavailable)
		return
	}
	edb := s.dataDB.EasyDB()
	driver := s.dataDB.Driver()
	tables := make([]TableStat, 0, 16)
	var totalBytes int64

	switch driver {
	case "mysql", "postgres":
		txt, err := s.dataDB.SQL("db_stats.sql") // 三方言同名脚本，各自方言语法的表统计查询
		if err != nil {
			http.Error(w, "读取空间统计脚本失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）："+err.Error(), http.StatusInternalServerError)
			return
		}
		var rows []map[string]any
		if err := edb.GetMany(txt, &rows); err != nil {
			http.Error(w, "空间统计查询失败："+err.Error()+"；请确认数据连接正常后重试", http.StatusInternalServerError)
			return
		}
		for _, row := range rows {
			tables = append(tables, TableStat{
				Name:    normStr(row["name"]),
				Comment: normStr(row["comment"]),
				Bytes:   mapInt64(row, "bytes"),
			})
		}
		totalBytes = sumBytes(tables)
	default: // sqlite（含未知驱动回落：按 sqlite 系统表语义查询）
		tableSQL, err := s.dataDB.SQL("schema_query_tables.sql")
		if err != nil {
			http.Error(w, "读取表清单脚本失败："+err.Error(), http.StatusInternalServerError)
			return
		}
		var err2 error
		tables, totalBytes, err2 = sqliteTableStats(edb, tableSQL)
		if err2 != nil {
			http.Error(w, "空间统计查询失败："+err2.Error()+"；请确认数据连接正常后重试", http.StatusInternalServerError)
			return
		}
	}

	// 条数统一动态 COUNT(*)：三方言语义一致且为精确值（系统表的 rows 列只是估算）
	if listSQL, err := s.dataDB.SQL("schema_query_tables.sql"); err == nil {
		var names []map[string]any
		if err := edb.GetMany(listSQL, &names); err == nil {
			counts := make(map[string]int64, len(names))
			for _, n := range names {
				name := normStr(n["table_name"])
				counts[name] = countRows(edb, driver, name)
			}
			for i := range tables {
				tables[i].Rows = counts[tables[i].Name]
			}
		}
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	_ = writeJSON(w, map[string]any{"driver": driver, "total_bytes": totalBytes, "tables": tables}, http.StatusOK)
}

// countRows 单表精确条数（动态 COUNT(*)；表名为系统表枚举出的标识符，非用户输入）。
func countRows(edb *easydb.EasyDb, driver, table string) int64 {
	if table == "" {
		return 0
	}
	var q string
	switch driver {
	case "mysql":
		q = "SELECT COUNT(*) AS cnt FROM `" + table + "`"
	default: // postgres / sqlite 均接受双引号标识符
		q = `SELECT COUNT(*) AS cnt FROM "` + table + `"`
	}
	var rows []map[string]any
	if err := edb.GetMany(q, &rows); err != nil || len(rows) == 0 {
		return 0
	}
	return mapInt64(rows[0], "cnt")
}

// sqliteTableStats SQLite 方言：表清单（sqlite_master）+ 逐表占用（dbstat 按页聚合，不可用时置 0）
// + 总占用（page_count×page_size，库级口径）。
func sqliteTableStats(edb *easydb.EasyDb, tableSQL string) ([]TableStat, int64, error) {
	tables := make([]TableStat, 0, 16)
	var total int64
	var rows []map[string]any
	// 总占用：page_count × page_size
	if err := edb.GetMany("SELECT (SELECT page_count FROM pragma_page_count) * (SELECT page_size FROM pragma_page_size) AS total", &rows); err == nil && len(rows) > 0 {
		total = mapInt64(rows[0], "total")
	}
	// 表清单 + 逐表占用（dbstat 虚表不可用时逐表占用为 0，仅总占用保真）
	if err := edb.GetMany(tableSQL, &rows); err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		name := normStr(row["table_name"])
		if name == "" {
			continue
		}
		b := int64(0)
		var st []map[string]any
		if err := edb.GetMany("SELECT COALESCE(SUM(pgsize), 0) AS b FROM dbstat WHERE name = ?", &st, name); err == nil && len(st) > 0 {
			b = mapInt64(st[0], "b")
		}
		tables = append(tables, TableStat{Name: name, Bytes: b})
	}
	return tables, total, nil
}

// sumBytes 逐表占用合计。
func sumBytes(tables []TableStat) int64 {
	var n int64
	for _, t := range tables {
		n += t.Bytes
	}
	return n
}

// normStr 驱动返回值归一为字符串（MySQL 驱动对 TEXT 列可能返回 []byte）。
func normStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}
