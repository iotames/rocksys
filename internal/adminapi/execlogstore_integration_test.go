//go:build integration

// SQL 执行审计存储集成测试：对真实 MySQL / PostgreSQL 全流程验证 sql_exec_log 方言脚本
// （建表 + 索引幂等 + 批量落库 + 分页查询/计数）。经环境变量门控，与 internal/db 现有约定一致：
//
//	MYSQL_TEST_DSN="<USER>:<PASSWORD>@tcp(<HOST>:3306)/<DB>" go test -tags integration -run TestExecLogStoreMySQL ./internal/adminapi/
//	PG_TEST_DSN="host=<HOST> user=<USER> password=<PASSWORD> dbname=<DB> sslmode=disable" go test -tags integration -run TestExecLogStorePG ./internal/adminapi/
//
// 使用带 _it_test 后缀的临时表名，结束后 DROP 清理，不触碰库内既有表。
package adminapi

import (
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"

	"rocksys/internal/db"
)

// testExecLogStore 全流程断言：建表（幂等）→ 落一批（含失败行）→ 计数 → 分页查询。
func testExecLogStore(t *testing.T, d *db.DB) {
	t.Helper()
	s := newExecLogStore(d.EasyDB(), d)
	if s == nil {
		t.Fatal("存储构造不应返回 nil")
	}
	s.tableName = "sql_exec_log_it_test" // 临时表名，测试结束 DROP

	batch := "fedcba98765432100123456789abcdef"
	entries := []*ExecLogEntry{
		{Time: time.Now().UTC(), BatchID: batch, Seq: 1, SQLText: "CREATE TABLE it_probe (id INTEGER)",
			OK: true, RowsAffected: 0, DurationMS: 5, ClientIP: "127.0.0.1", Source: "webui"},
		{Time: time.Now().UTC(), BatchID: batch, Seq: 2, SQLText: "CREATE TABLE it_probe (id INTEGER)",
			OK: false, Error: "table already exists", DurationMS: 1, ClientIP: "::1", Source: "webui"},
	}
	if err := s.Insert(entries); err != nil {
		t.Fatalf("落审计批次: %v", err)
	}
	// 索引脚本幂等：MySQL 的 CREATE INDEX 无 IF NOT EXISTS，二次 Insert（复跑 ensure）不应报错
	if err := s.Insert(entries[:1]); err != nil {
		t.Fatalf("重复落库（索引幂等）: %v", err)
	}

	total, err := s.Count()
	if err != nil {
		t.Fatalf("计数: %v", err)
	}
	if total != 3 {
		t.Fatalf("总数应为 3，got %d", total)
	}

	items, err := s.Query(10, 0)
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("应为 3 条，got %d", len(items))
	}
	first := items[0]
	if !first.OK || first.Seq != 1 || first.BatchID != batch || first.ClientIP != "127.0.0.1" || first.Source != "webui" {
		t.Fatalf("首条字段不符: %+v", first)
	}
	failed := items[1]
	if failed.OK || failed.Error != "table already exists" || failed.DurationMS != 1 {
		t.Fatalf("失败记录字段不符: %+v", failed)
	}
	if failed.Time == "" {
		t.Fatal("time 应归一为非空字符串（RFC3339）")
	}
}

func TestExecLogStoreMySQL(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过 MySQL 集成测试")
	}
	d, err := db.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("Open(mysql) err: %v", err)
	}
	defer func() { _ = d.Close() }()
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS sql_exec_log_it_test") })
	testExecLogStore(t, d)
}

func TestExecLogStorePG(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	d, err := db.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Open(postgres) err: %v", err)
	}
	defer func() { _ = d.Close() }()
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS sql_exec_log_it_test") })
	testExecLogStore(t, d)
}
