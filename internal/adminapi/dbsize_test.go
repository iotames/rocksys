// dbsize_test.go：数据库空间统计端点单测（sqlite 内存库，真实脚本建表）。
package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestDBSizeEndpoint 建表后统计：total_bytes>0、表清单齐全、条数为精确 COUNT(*)。
func TestDBSizeEndpoint(t *testing.T) {
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
	if bl.Bytes <= 0 {
		t.Errorf("dbstat 可用时逐表占用应 > 0，got %d", bl.Bytes)
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
