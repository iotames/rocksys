//go:build integration

// schema_sync_integration_test.go：表结构同步真库端到端集成测试（环境变量门控）。
// PG_TEST_DSN / MYSQL_TEST_DSN 设置后运行（与 pg/mysql_integration_test.go 同门控）：
// 旧版表 → diff（B/C/D）→ 生成 SQL → 逐条执行 → 复核零差异，验证三方言真实链路。
package db_test

import (
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql" // 注册 mysql 驱动
	_ "github.com/lib/pq"              // 注册 postgres 驱动

	"rocksys/internal/db"
)

// runSchemaSyncTest 真库端到端同步链路（driver 方言目录名）。
func runSchemaSyncTest(t *testing.T, driver, dsn string) {
	t.Helper()
	lockSharedDevDB(t) // devdb 共享库互斥：F 级全库扫描与其它包的真库建表测试互踩
	d, err := db.Open(driver, dsn)
	if err != nil {
		t.Fatalf("Open(%s) err: %v", driver, err)
	}
	defer d.Close()

	const table = "ip_blacklist"
	cleanup := func() {
		_, _ = d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS " + table)
	}
	cleanup()
	// ★ defer 而非 t.Cleanup：t.Cleanup 晚于 defer d.Close() 执行，DROP 会落在
	// 已关闭连接上被静默吞掉 → 残留一个缺唯一约束的旧版表污染共享库。
	defer cleanup()

	specs := []db.TableSpec{{Table: table, CreateScript: "ip_blacklist_create_table.sql", IndexScript: "ip_blacklist_create_index.sql"}}

	// 旧版表：缺 block_type/hit_count/expires_at/deleted_at/updated_at（B 级）；缺全部索引（D 级）
	oldDDL := map[string]string{
		"sqlite":   "CREATE TABLE ip_blacklist (id INTEGER PRIMARY KEY AUTOINCREMENT, ip TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL)",
		"mysql":    "CREATE TABLE ip_blacklist (id BIGINT AUTO_INCREMENT PRIMARY KEY, ip VARCHAR(45) NOT NULL, title VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL)",
		"postgres": "CREATE TABLE ip_blacklist (id BIGSERIAL PRIMARY KEY, ip TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL)",
	}
	if _, err := d.EasyDB().GetSqlDB().Exec(oldDDL[driver]); err != nil {
		t.Fatalf("建旧版表: %v", err)
	}

	// 第一次检查：应产出 B/D 自动项（类型归一后无 E 误报）
	items, err := db.DiffSchema(context.Background(), d, specs)
	if err != nil {
		t.Fatalf("DiffSchema: %v", err)
	}
	var auto []db.DiffItem
	for _, it := range items {
		if it.Auto {
			auto = append(auto, it)
		}
		if it.Level == "E" {
			t.Errorf("真实旧版表不应有 E 级误报（类型归一化失真）: %+v", it)
		}
	}
	if len(auto) < 5 { // 5 个缺列 + 1 段索引（2 索引项）
		t.Fatalf("旧版表应产出 ≥5 个自动差异项，got %d: %+v", len(auto), items)
	}
	sqlText, err := db.GenerateSQL(items, specs, d)
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if !strings.Contains(sqlText, "ADD COLUMN") || !strings.Contains(sqlText, "CREATE INDEX") {
		t.Fatalf("生成 SQL 应含 ADD COLUMN 与 CREATE INDEX:\n%s", sqlText)
	}

	// 执行生成 SQL（逐条，与 /admin/db/exec 同语义）
	for _, stmt := range db.SplitStatements(sqlText) {
		if _, err := d.EasyDB().GetSqlDB().Exec(stmt); err != nil {
			t.Fatalf("执行生成 SQL 失败（%s）: %v\n%s", driver, err, stmt)
		}
	}

	// 复核：零差异
	items, err = db.DiffSchema(context.Background(), d, specs)
	if err != nil {
		t.Fatalf("复核 DiffSchema: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("同步后应零差异，got %d 项: %+v", len(items), items)
	}
}

// TestSchemaSyncPostgres 真库端到端（PG_TEST_DSN 门控）。
func TestSchemaSyncPostgres(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	runSchemaSyncTest(t, "postgres", dsn)
}

// TestSchemaSyncMySQL 真库端到端（MYSQL_TEST_DSN 门控）。
func TestSchemaSyncMySQL(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过 MySQL 集成测试")
	}
	runSchemaSyncTest(t, "mysql", dsn)
}
