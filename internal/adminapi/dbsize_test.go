// dbsize_test.go：数据库空间统计端点单测（sqlite 内存库，真实脚本建表）。
package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// resetDBSizeCache 清空单表占用进程内缓存：缓存是包级全局、跨用例共享，
// 不重置会让「首算 cached=false」之类的断言依赖用例执行顺序（潜伏的顺序敏感）。
func resetDBSizeCache() {
	dbSizeCacheMu.Lock()
	dbSizeCache = map[string]sizeCacheEntry{}
	dbSizeCacheMu.Unlock()
}

// TestDBSizeEndpoint 建表后统计：total_bytes>0、表清单齐全、条数为精确 COUNT(*)。
func TestDBSizeEndpoint(t *testing.T) {
	resetDBSizeCache()
	s, d := setupSchemaServer(t, testSpecs())
	execAll(t, d, "shield_event_create_table.sql", "shield_event_create_index.sql",
		"ip_blacklist_create_table.sql", "ip_blacklist_create_index.sql")
	// 插入已知行数，验证动态 COUNT(*) 精确性（ip 为 UNIQUE 列，逐行递增）
	for i := 0; i < 3; i++ {
		stmt := `INSERT INTO ip_blacklist (ip, title, created_at, updated_at) VALUES ('10.0.0.` +
			string(rune('0'+i)) + `', 't', '2026-09-05 00:00:00', '2026-09-05 00:00:00')`
		if _, err := d.EasyDB().Exec(stmt); err != nil {
			t.Fatalf("插入测试行: %v", err)
		}
	}

	rec := callHandler(t, s.handleDBSize, http.MethodGet, "/admin/db/size", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Driver     string      `json:"driver"`
		TotalBytes int64       `json:"total_bytes"`
		Tables     []TableStat `json:"tables"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if res.Driver != "sqlite" {
		t.Errorf("driver 应为 sqlite，got %s", res.Driver)
	}
	if res.TotalBytes <= 0 {
		t.Errorf("total_bytes 应 > 0（page_count×page_size），got %d", res.TotalBytes)
	}
	byName := map[string]TableStat{}
	for _, tb := range res.Tables {
		byName[tb.Name] = tb
	}
	// admin_users 由 New() 内 userstore 建表，理应出现在清单中
	if _, ok := byName["admin_users"]; !ok {
		t.Fatalf("表清单应含 admin_users，got %d 张: %v", len(res.Tables), res.Tables)
	}
	bl, ok := byName["ip_blacklist"]
	if !ok {
		t.Fatalf("表清单应含 ip_blacklist，got %d 张", len(res.Tables))
	}
	if bl.Rows != 3 {
		t.Errorf("ip_blacklist 条数应为精确 3（动态 COUNT(*)），got %d", bl.Rows)
	}
	if bl.Bytes != 0 || bl.BytesKnown {
		t.Errorf("逐表占用默认不计算（dbstat 全库页遍历代价高），应 bytes=0 且 bytes_known=false，got bytes=%d known=%v", bl.Bytes, bl.BytesKnown)
	}

	// 按需计算单表占用：调用 table_size 后应给出已计算值，且再查一次为缓存命中
	rec2 := callHandler(t, s.handleDBTableSize, http.MethodGet, "/admin/db/table_size?table=ip_blacklist", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("table_size 状态码 %d: %s", rec2.Code, rec2.Body.String())
	}
	var one struct {
		Table  string `json:"table"`
		Bytes  int64  `json:"bytes"`
		Data   int64  `json:"data_bytes"`
		Index  int64  `json:"index_bytes"`
		Cached bool   `json:"cached"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &one); err != nil {
		t.Fatalf("解析 table_size 响应: %v", err)
	}
	if one.Table != "ip_blacklist" || one.Bytes <= 0 || one.Cached {
		t.Errorf("首算应返回 ip_blacklist 的占用且 cached=false，got %+v", one)
	}
	// 拆分口径：数据（表 B-tree）+ 索引（各索引 B-tree）= 合计；ip_blacklist 有 ip 唯一索引
	if one.Data <= 0 {
		t.Errorf("数据占用应 > 0，got %+v", one)
	}
	if one.Index <= 0 {
		t.Errorf("索引占用应 > 0（建有 ip_blacklist_create_index.sql 的索引），got %+v", one)
	}
	if one.Data+one.Index != one.Bytes {
		t.Errorf("数据+索引应等于合计，got data=%d index=%d bytes=%d", one.Data, one.Index, one.Bytes)
	}
	rec3 := callHandler(t, s.handleDBTableSize, http.MethodGet, "/admin/db/table_size?table=ip_blacklist", "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("table_size 二次调用状态码 %d", rec3.Code)
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &one); err != nil {
		t.Fatalf("解析 table_size 二次响应: %v", err)
	}
	if !one.Cached || one.Bytes <= 0 {
		t.Errorf("二次调用应命中缓存，got %+v", one)
	}
	// 库级 size 再查：已计算的表应带上缓存值
	rec4 := callHandler(t, s.handleDBSize, http.MethodGet, "/admin/db/size", "")
	if err := json.Unmarshal(rec4.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析二次 size 响应: %v", err)
	}
	for _, tb := range res.Tables {
		if tb.Name == "ip_blacklist" && (!tb.BytesKnown || tb.Bytes <= 0) {
			t.Errorf("已计算过的表在 size 清单中应带缓存值，got %+v", tb)
		}
	}
}

// TestDBTableSizeGuard 表名白名单：不存在的表 404、缺参数 400（防任意标识符拼接入 SQL）。
func TestDBTableSizeGuard(t *testing.T) {
	resetDBSizeCache()
	s, d := setupSchemaServer(t, testSpecs())
	execAll(t, d, "ip_blacklist_create_table.sql", "ip_blacklist_create_index.sql")

	if rec := callHandler(t, s.handleDBTableSize, http.MethodGet, "/admin/db/table_size", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("缺 table 参数应 400，got %d", rec.Code)
	}
	if rec := callHandler(t, s.handleDBTableSize, http.MethodGet, "/admin/db/table_size?table=no_such_table", ""); rec.Code != http.StatusNotFound {
		t.Errorf("表不存在应 404，got %d", rec.Code)
	}
	if rec := callHandler(t, s.handleDBTableSize, http.MethodGet, "/admin/db/table_size?table=ip_blacklist%22%3BDROP+TABLE+ip_blacklist%3B--", ""); rec.Code != http.StatusNotFound {
		t.Errorf("非法表名应 404（白名单拒绝），got %d", rec.Code)
	}
}

// TestDBTableSizeDBUnavailable 表清单查询失败（数据连接不可用）应报 500 并指出去查数据连接，
// 不得误报成 404「表不存在」——那是错误的出路指引（回归：曾把查询失败与查无此表都当 false）。
func TestDBTableSizeDBUnavailable(t *testing.T) {
	resetDBSizeCache()
	s, d := setupSchemaServer(t, testSpecs())
	execAll(t, d, "ip_blacklist_create_table.sql")
	if err := d.Close(); err != nil {
		t.Fatalf("关闭数据连接: %v", err)
	}
	rec := callHandler(t, s.handleDBTableSize, http.MethodGet, "/admin/db/table_size?table=ip_blacklist", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("数据连接不可用应 500（非误报 404 表不存在），got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDBSizeNoDB 未装配数据连接时 503。
func TestDBSizeNoDB(t *testing.T) {
	s := New("0.0.0.0:19527", nil, nil, nil)
	rec := callHandler(t, s.handleDBSize, http.MethodGet, "/admin/db/size", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("无数据连接应 503，got %d", rec.Code)
	}
}
