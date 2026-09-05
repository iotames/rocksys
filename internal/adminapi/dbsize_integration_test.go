//go:build integration

// 数据库空间统计集成测试：对真实 MySQL / PostgreSQL 验证 /admin/db/size 全流程
// （方言统计脚本 + 动态 COUNT(*) + 归一化输出）。环境变量门控与 internal/db 现有约定一致：
//
//	MYSQL_TEST_DSN="<USER>:<PASSWORD>@tcp(<HOST>:3306)/<DB>" go test -tags integration -run TestDBSizeMySQL ./internal/adminapi/
//	PG_TEST_DSN="host=<HOST> user=<USER> password=<PASSWORD> dbname=<DB> sslmode=disable" go test -tags integration -run TestDBSizePG ./internal/adminapi/
//
// 使用带 _it_test 后缀的临时表，结束后 DROP 清理，不触碰库内既有表。
package adminapi

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"rocksys/internal/db"
)

func testDBSize(t *testing.T, d *db.DB) {
	t.Helper()
	s := New("0.0.0.0:19527", nil, nil, d.EasyDB())
	s.SetSQLSource(d)
	s.SetTableSpecs(d, nil) // 空间统计依赖 dataDB（脚本读取走 SQLSource）

	// 建一张带备注的临时表并插 2 行（MySQL 走 COMMENT，PG 走 COMMENT ON）
	if _, err := d.EasyDB().Exec("DROP TABLE IF EXISTS db_size_it_test"); err != nil {
		t.Fatalf("清理旧临时表: %v", err)
	}
	if _, err := d.EasyDB().Exec("CREATE TABLE db_size_it_test (id INTEGER PRIMARY KEY, title VARCHAR(64) NOT NULL DEFAULT '')"); err != nil {
		t.Fatalf("建临时表: %v", err)
	}
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS db_size_it_test") })
	if d.Driver() == "mysql" {
		if _, err := d.EasyDB().Exec("ALTER TABLE db_size_it_test COMMENT = '集成测试表'"); err != nil {
			t.Fatalf("设表备注: %v", err)
		}
	} else {
		if _, err := d.EasyDB().Exec("COMMENT ON TABLE db_size_it_test IS '集成测试表'"); err != nil {
			t.Fatalf("设表备注: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := d.EasyDB().Exec("INSERT INTO db_size_it_test (id, title) VALUES (" + string(rune('1'+i)) + ", 'x')"); err != nil {
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
	byName := map[string]TableStat{}
	for _, tb := range res.Tables {
		byName[tb.Name] = tb
	}
	st, ok := byName["db_size_it_test"]
	if !ok {
		t.Fatalf("表清单应含 db_size_it_test，got %d 张", len(res.Tables))
	}
	if st.Rows != 2 {
		t.Errorf("条数应为精确 2（动态 COUNT(*)），got %d", st.Rows)
	}
	if st.Bytes <= 0 {
		t.Errorf("占用空间应 > 0（系统表统计），got %d", st.Bytes)
	}
	if st.Comment != "集成测试表" {
		t.Errorf("表备注应为「集成测试表」，got %q", st.Comment)
	}
	if res.TotalBytes < st.Bytes {
		t.Errorf("总占用(%d)不应小于单表占用(%d)", res.TotalBytes, st.Bytes)
	}
}

func TestDBSizeMySQL(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过 MySQL 集成测试")
	}
	d, err := db.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("Open(mysql) err: %v", err)
	}
	defer func() { _ = d.Close() }()
	testDBSize(t, d)
}

func TestDBSizePG(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	d, err := db.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Open(postgres) err: %v", err)
	}
	defer func() { _ = d.Close() }()
	testDBSize(t, d)
}
