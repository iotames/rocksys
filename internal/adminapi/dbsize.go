// dbsize.go：数据库空间占用端点。
//
//	GET /admin/db/size              —— 库内全部基础表的清单/备注/条数 + 库级总占用（毫秒级，逐表占用只取缓存）
//	GET /admin/db/table_size?table= —— 单表精确占用（按需计算，结果进进程内缓存）
//
// 口径与性能（2026-09-11 优化）：
//   - 条数为精确值：对每张表动态执行 COUNT(*)（走覆盖索引，百万行约数十毫秒；MySQL/PG 的
//     information_schema 行数是估算值，不满足运营精确口径）。
//   - 逐表占用**默认不计算**：SQLite 无系统表可用，逐表占用必须走 dbstat 虚表按页聚合，
//     而 dbstat 每次查询都要遍历整库页树（586MB 库实测首次约 20 秒；MySQL 的
//     DATA_LENGTH+INDEX_LENGTH 与 PG 的 pg_total_relation_size 则毫秒级）。
//     故 SQLite 下默认返回 bytes_known=false，由前端逐表按需触发 /admin/db/table_size。
//   - 单表精确占用结果进进程内缓存（dbSizeCacheTTL），重复查看/刷新即时返回。
//   - 库级总占用：SQLite 取 page_count×page_size（毫秒级，含空闲页）；MySQL/PG 取系统表合计。
package adminapi

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iotames/easydb"
)

// dbSizeCacheTTL 单表占用缓存 TTL：表空间变化缓慢，10 分钟内复用足够（非配置项，实现细节）。
const dbSizeCacheTTL = 10 * time.Minute

// TableStat 单张表的空间占用统计。
// 表占用的主要组成：表数据（SQLite 表 B-tree 页，含溢出页；MySQL 聚簇索引 DATA_LENGTH；
// PG pg_relation_size）+ 索引（SQLite 各索引 B-tree；MySQL INDEX_LENGTH；PG pg_indexes_size）。
// 未纳入的次要项：MySQL DATA_FREE（碎片）、PG TOAST（含在 bytes 内）、SQLite freelist（库级空闲页）。
type TableStat struct {
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Rows    int64  `json:"rows"`
	Bytes   int64  `json:"bytes"` // 合计（SQLite 数据+索引；MySQL 数据+索引；PG 表+索引+TOAST）
	// DataBytes/IndexBytes 合计的拆分：数据占用 / 索引占用。
	DataBytes  int64 `json:"data_bytes"`
	IndexBytes int64 `json:"index_bytes"`
	// BytesKnown 占用空间是否为已计算值：false=尚未计算（前端显示「未计算」+ 按需触发计算）。
	BytesKnown bool `json:"bytes_known"`
}

// sizeEntry 单表占用缓存项（数据/索引/合计三元组）。
type sizeEntry struct {
	data  int64
	index int64
	total int64
}

// sizeCacheEntry 单表占用缓存项（带时间戳）。
type sizeCacheEntry struct {
	entry sizeEntry
	at    time.Time
}

// 单表占用缓存（进程内；表空间变化缓慢，无需持久化）。
var (
	dbSizeCacheMu sync.Mutex
	dbSizeCache   = map[string]sizeCacheEntry{}
)

func sizeCacheGet(table string) (sizeEntry, bool) {
	dbSizeCacheMu.Lock()
	defer dbSizeCacheMu.Unlock()
	e, ok := dbSizeCache[table]
	if !ok || time.Since(e.at) > dbSizeCacheTTL {
		return sizeEntry{}, false
	}
	return e.entry, true
}

func sizeCachePut(table string, e sizeEntry) {
	dbSizeCacheMu.Lock()
	defer dbSizeCacheMu.Unlock()
	dbSizeCache[table] = sizeCacheEntry{entry: e, at: time.Now()}
}

// handleDBSize 库内全部基础表清单与库级总占用（只读，不落库）。
// 逐表占用只取缓存：未计算过的表返回 bytes_known=false，由前端按需调 handleDBTableSize。
func (s *AdminServer) handleDBSize(w http.ResponseWriter, r *http.Request) {
	if s.dataDB == nil {
		http.Error(w, "空间统计不可用：数据连接未装配", http.StatusServiceUnavailable)
		return
	}
	edb := s.dataDB.EasyDB()
	driver := s.dataDB.Driver()
	tables := make([]TableStat, 0, 16)
	var totalBytes int64
	// sysBytes=true：方言系统表直接给出逐表占用（MySQL/PG，毫秒级），无需按需计算
	sysBytes := false

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
			data := mapInt64(row, "data_bytes")
			idx := mapInt64(row, "index_bytes")
			total := mapInt64(row, "bytes")
			if total == 0 && (data > 0 || idx > 0) {
				total = data + idx
			}
			tables = append(tables, TableStat{
				Name:       normStr(row["name"]),
				Comment:    normStr(row["comment"]),
				Bytes:      total,
				DataBytes:  data,
				IndexBytes: idx,
				BytesKnown: true,
			})
		}
		totalBytes = sumBytes(tables)
		sysBytes = true
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

	// 条数统一动态 COUNT(*)：三方言语义一致且为精确值（系统表的 rows 列只是估算）。
	// 逐表占用：系统表方言已知；SQLite 仅取缓存命中值（未命中保持 bytes_known=false）。
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
				if !sysBytes {
					if e, ok := sizeCacheGet(tables[i].Name); ok {
						tables[i].Bytes = e.total
						tables[i].DataBytes = e.data
						tables[i].IndexBytes = e.index
						tables[i].BytesKnown = true
					}
				}
			}
		}
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	_ = writeJSON(w, map[string]any{"driver": driver, "total_bytes": totalBytes, "tables": tables}, http.StatusOK)
}

// handleDBTableSize 单表精确占用（按需计算 + 进程内缓存）。
// 表名必须来自库内实际表清单（白名单校验），拒绝任意标识符拼接入 SQL。
func (s *AdminServer) handleDBTableSize(w http.ResponseWriter, r *http.Request) {
	if s.dataDB == nil {
		http.Error(w, "空间统计不可用：数据连接未装配", http.StatusServiceUnavailable)
		return
	}
	table := r.URL.Query().Get("table")
	if table == "" {
		http.Error(w, "缺少 table 参数（表名取自表清单）", http.StatusBadRequest)
		return
	}
	// 表清单查询失败（连接不可用等）与「表不在清单里」必须区分：
	// 前者报 500 并指出去查数据连接，后者才是 404（否则断连时提示「表不存在」，出路是错的）。
	if exists, err := s.tableExists(table); err != nil {
		http.Error(w, "表清单查询失败："+err.Error()+"；请确认数据连接正常后重试", http.StatusInternalServerError)
		return
	} else if !exists {
		http.Error(w, "表不存在："+table+"（表名须取自表清单）", http.StatusNotFound)
		return
	}
	if e, ok := sizeCacheGet(table); ok {
		_ = writeJSON(w, map[string]any{
			"table": table, "bytes": e.total, "data_bytes": e.data, "index_bytes": e.index, "cached": true,
		}, http.StatusOK)
		return
	}
	e, err := s.tableBytes(s.dataDB.Driver(), table)
	if err != nil {
		http.Error(w, "单表占用统计失败："+err.Error()+"；可稍后重试", http.StatusInternalServerError)
		return
	}
	sizeCachePut(table, e)
	_ = writeJSON(w, map[string]any{
		"table": table, "bytes": e.total, "data_bytes": e.data, "index_bytes": e.index, "cached": false,
	}, http.StatusOK)
}

// tableExists 表名白名单校验（库内实际表清单，SQLite 走 sqlite_master，MySQL/PG 走系统表）。
// 第二个返回值为查询错误（脚本读取失败或清单查询失败），与「清单里没有该表」严格区分。
func (s *AdminServer) tableExists(table string) (bool, error) {
	listSQL, err := s.dataDB.SQL("schema_query_tables.sql")
	if err != nil {
		return false, err
	}
	var rows []map[string]any
	if err := s.dataDB.EasyDB().GetMany(listSQL, &rows); err != nil {
		return false, err
	}
	for _, row := range rows {
		if normStr(row["table_name"]) == table {
			return true, nil
		}
	}
	return false, nil
}

// tableBytes 单表占用拆分（数据 / 索引 / 合计），方言口径：
//   - sqlite：dbstat 一次遍历，按对象名把页归属到「表 B-tree（含溢出页）」与「该表各索引 B-tree」；
//     索引名经 sqlite_master（tbl_name）映射，主键/唯一约束的 sqlite_autoindex_* 同样在册；
//   - mysql：information_schema 的 DATA_LENGTH（聚簇索引即数据）与 INDEX_LENGTH（二级索引）；
//   - postgres：pg_relation_size（表堆）与 pg_indexes_size（索引），合计取 pg_total_relation_size（含 TOAST）。
func (s *AdminServer) tableBytes(driver, table string) (sizeEntry, error) {
	edb := s.dataDB.EasyDB()
	switch driver {
	case "mysql":
		var rows []map[string]any
		q := "SELECT COALESCE(DATA_LENGTH, 0) AS data_bytes, COALESCE(INDEX_LENGTH, 0) AS index_bytes " +
			"FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?"
		if err := edb.GetMany(q, &rows, table); err != nil {
			return sizeEntry{}, err
		}
		if len(rows) == 0 {
			return sizeEntry{}, nil
		}
		e := sizeEntry{data: mapInt64(rows[0], "data_bytes"), index: mapInt64(rows[0], "index_bytes")}
		e.total = e.data + e.index
		return e, nil
	case "postgres":
		var rows []map[string]any
		q := "SELECT pg_relation_size($1::regclass) AS data_bytes, pg_indexes_size($1::regclass) AS index_bytes, " +
			"pg_total_relation_size($1::regclass) AS total"
		if err := edb.GetMany(q, &rows, table); err != nil {
			return sizeEntry{}, err
		}
		if len(rows) == 0 {
			return sizeEntry{}, nil
		}
		e := sizeEntry{
			data:  mapInt64(rows[0], "data_bytes"),
			index: mapInt64(rows[0], "index_bytes"),
			total: mapInt64(rows[0], "total"),
		}
		if e.total == 0 {
			e.total = e.data + e.index
		}
		return e, nil
	default: // sqlite
		return sqliteTableBytes(edb, table)
	}
}

// sqliteTableBytes SQLite 单表占用拆分：先取该表索引名（sqlite_master.tbl_name），
// 再以**一次** dbstat 遍历按对象名聚合出表数据与索引占用（dbstat 需遍历全库页树，遍历次数越少越好）。
// 表数据含表 B-tree 的 leaf/internal 及溢出页（overflow 页的 name 归属该对象）；索引为全部索引 B-tree 之和。
func sqliteTableBytes(edb *easydb.EasyDb, table string) (sizeEntry, error) {
	var idxRows []map[string]any
	// sqlite_autoindex_*（主键/唯一约束隐式索引）同样记录在 sqlite_master，故一并纳入
	if err := edb.GetMany("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?", &idxRows, table); err != nil {
		return sizeEntry{}, err
	}
	var idxNames []string
	for _, r := range idxRows {
		if n := normStr(r["name"]); n != "" {
			idxNames = append(idxNames, n)
		}
	}
	// 表数据：name = 表名（含 overflow 页）
	var rows []map[string]any
	if err := edb.GetMany("SELECT COALESCE(SUM(pgsize), 0) AS b FROM dbstat WHERE name = ?", &rows, table); err != nil {
		return sizeEntry{}, err
	}
	e := sizeEntry{}
	if len(rows) > 0 {
		e.data = mapInt64(rows[0], "b")
	}
	if len(idxNames) == 0 {
		e.total = e.data
		return e, nil
	}
	// 索引：合并为一条 SQL（name IN (...)），避免逐索引重复全库遍历
	ph := strings.TrimSuffix(strings.Repeat("?,", len(idxNames)), ",")
	args := make([]any, 0, len(idxNames))
	for _, n := range idxNames {
		args = append(args, n)
	}
	var irows []map[string]any
	if err := edb.GetMany("SELECT COALESCE(SUM(pgsize), 0) AS b FROM dbstat WHERE name IN ("+ph+")", &irows, args...); err != nil {
		return sizeEntry{}, err
	}
	if len(irows) > 0 {
		e.index = mapInt64(irows[0], "b")
	}
	e.total = e.data + e.index
	return e, nil
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

// sqliteTableStats SQLite 方言：表清单（sqlite_master）+ 库级总占用（page_count×page_size）。
// 逐表占用不在此计算（dbstat 全库页遍历，大库首次约 20 秒），由 handleDBTableSize 按需触发。
func sqliteTableStats(edb *easydb.EasyDb, tableSQL string) ([]TableStat, int64, error) {
	tables := make([]TableStat, 0, 16)
	var total int64
	var rows []map[string]any
	// 总占用：page_count × page_size（毫秒级）
	if err := edb.GetMany("SELECT (SELECT page_count FROM pragma_page_count) * (SELECT page_size FROM pragma_page_size) AS total", &rows); err == nil && len(rows) > 0 {
		total = mapInt64(rows[0], "total")
	}
	if err := edb.GetMany(tableSQL, &rows); err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		name := normStr(row["table_name"])
		if name == "" {
			continue
		}
		tables = append(tables, TableStat{Name: name})
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
