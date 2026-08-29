//go:build integration

// schema_sync_acceptance_integration_test.go：数据库表结构同步功能验收测试（真库，环境变量门控）。
// 验收基准：docs/DB_SCHEMA_SYNC_PLAN.md §3（设计）与 §5（测试与验收）——
//  1. 全新库全清单（7 表，文件名≠表名）A 级闭环：检查 → 生成建表 SQL → 逐条执行 → 复核零差异；
//  2. 单索引缺失 D 级：只生成缺失索引的单条语句（不整份重放），执行后零差异；
//  3. catalog 真实可查：自增/默认值/可空识别（pg pg_node_tree LIKE 回归、mysql EXTRA、sqlite PRAGMA）。
//
// 运行：
//
//	PG_TEST_DSN="postgresql://dev:dev123456@127.0.0.1:5432/devdb?sslmode=disable" \
//	MYSQL_TEST_DSN="dev:dev123456@tcp(127.0.0.1:3306)/devdb?parseTime=true" \
//	  go test -tags integration -run 'TestSchemaSyncAcceptance' ./internal/db/
package db_test

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"rocksys/internal/db"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// lockSharedDevDB 跨包互斥锁：devdb 为共享开发库，go test 多包并行时，本包的
// 全清单验收（DROP/CREATE 7 张规范表）会与其它包（如 plugins/shield 的 attack_archive
// 建表测试）互踩同名表，以文件锁把冲突面串行化（仅测试用）。
func lockSharedDevDB(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(os.TempDir()+"/rocksys-devdb-it.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开互斥锁文件失败: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("加互斥锁失败: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

// pgDSN / mysqlDSN 真库连接串（环境变量门控）。
func pgDSN() string { return os.Getenv("PG_TEST_DSN") }

func mysqlDSN() string { return os.Getenv("MYSQL_TEST_DSN") }

// acceptanceSpecs 全清单 7 表（与 cmd/rocksys/main.go 装配处一致；此处独立声明以便
// 在 internal/db 包内做端到端验收，一致性由 main_test.go 的防漏单测保障）。
func acceptanceSpecs(shieldEventTable string) []db.TableSpec {
	return []db.TableSpec{
		{Table: "admin_users", CreateScript: "admin_users_create_table.sql"},
		{Table: "ip_blacklist", CreateScript: "ip_blacklist_create_table.sql", IndexScript: "ip_blacklist_create_index.sql"},
		{Table: "ip_whitelist", CreateScript: "ip_whitelist_create_table.sql", IndexScript: "ip_whitelist_create_index.sql"},
		{Table: "attack_archive", CreateScript: "attack_archive_create_table.sql", IndexScript: "attack_archive_create_index.sql"},
		{Table: shieldEventTable, CreateScript: "shield_event_create_table.sql", IndexScript: "shield_event_create_index.sql"},
		{Table: "access_log", CreateScript: "access_log_create_table.sql", IndexScript: "access_log_create_index.sql"},
		{Table: "outbox", CreateScript: "mq_create_table.sql", IndexScript: "mq_create_index.sql"},
	}
}

// execAll 逐条执行（与 /admin/db/exec 同语义：拆句、遇错即停）。
func execAll(t *testing.T, d *db.DB, sqlText string) {
	t.Helper()
	stmts := db.SplitStatements(sqlText)
	if len(stmts) == 0 {
		t.Fatalf("生成 SQL 拆句后为空:\n%s", sqlText)
	}
	for _, stmt := range stmts {
		if _, err := d.EasyDB().GetSqlDB().Exec(stmt); err != nil {
			t.Fatalf("执行失败: %v\n语句: %s", err, stmt)
		}
	}
}

// runAcceptanceFreshDB 验收 1：全新库 7 表 A 级闭环。
func runAcceptanceFreshDB(t *testing.T, driver, dsn string) {
	t.Helper()
	d, err := db.Open(driver, dsn)
	if err != nil {
		t.Fatalf("Open(%s) err: %v", driver, err)
	}
	specs := acceptanceSpecs("shield_event")
	// 测试自清理：共享 devdb，跑完恢复原状（避免残留表污染其它测试的 F 级「多余表」断言）。
	// ★ d.Close() 必须在清理内最后调用（t.Cleanup 晚于 defer 执行，defer 先关连接会让 DROP 静默失败）。
	t.Cleanup(func() {
		for _, s := range specs {
			_, _ = d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS " + s.Table)
		}
		_, _ = d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS schema_sync_acceptance_t")
		_ = d.Close()
	})
	for _, s := range specs {
		if _, err := d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS " + s.Table); err != nil {
			t.Fatalf("清场 DROP %s: %v", s.Table, err)
		}
	}
	// 验收 3 的专用表也一并清场（此前失败运行可能遗留）
	if _, err := d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS schema_sync_acceptance_t"); err != nil {
		t.Fatalf("清场 DROP schema_sync_acceptance_t: %v", err)
	}

	// 第一次检查：7 表全缺 → A 级自动项
	items, err := db.DiffSchema(d, specs)
	if err != nil {
		t.Fatalf("DiffSchema: %v", err)
	}
	aCount := 0
	for _, it := range items {
		if it.Level == "A" && it.Auto && it.Object == it.Table {
			aCount++
		}
	}
	if aCount != len(specs) {
		t.Fatalf("全新库应产出 %d 个 A 级缺表项，got %d: %+v", len(specs), aCount, items)
	}
	sqlText, err := db.GenerateSQL(items, specs, d)
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	execAll(t, d, sqlText)

	// 复核：零差异
	items, err = db.DiffSchema(d, specs)
	if err != nil {
		t.Fatalf("复核 DiffSchema: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("建表后应零差异，got %d 项: %+v", len(items), items)
	}
}

// runAcceptanceSingleIndex 验收 2：单索引缺失 D 级只补缺失项。
func runAcceptanceSingleIndex(t *testing.T, driver, dsn string) {
	t.Helper()
	d, err := db.Open(driver, dsn)
	if err != nil {
		t.Fatalf("Open(%s) err: %v", driver, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	specs := acceptanceSpecs("shield_event")

	// 先建齐全部表与索引（先清场，保证跨次失败重跑幂等）
	for _, s := range specs {
		if _, err := d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS " + s.Table); err != nil {
			t.Fatalf("清场 DROP %s: %v", s.Table, err)
		}
	}
	for _, s := range specs {
		ddl, err := d.SQL(s.CreateScript)
		if err != nil {
			t.Fatalf("读建表脚本 %s: %v", s.CreateScript, err)
		}
		execAll(t, d, strings.ReplaceAll(ddl, "{table}", s.Table))
		if s.IndexScript == "" {
			continue
		}
		idx, err := d.SQL(s.IndexScript)
		if err != nil {
			t.Fatalf("读索引脚本 %s: %v", s.IndexScript, err)
		}
		for _, stmt := range db.SplitSQLStatements(strings.ReplaceAll(idx, "{table}", s.Table)) {
			if _, err := d.EasyDB().GetSqlDB().Exec(stmt); err != nil {
				t.Fatalf("建索引失败: %v\n%s", err, stmt)
			}
		}
	}

	const table = "ip_blacklist"
	// 删掉 ip_blacklist 的一个索引（block_type），应只产出该索引的 D 级项
	const dropIdx = "idx_ip_blacklist_block_type"
	switch driver {
	case "mysql":
		_, _ = d.EasyDB().GetSqlDB().Exec("DROP INDEX " + dropIdx + " ON " + table)
	case "postgres":
		_, _ = d.EasyDB().GetSqlDB().Exec("DROP INDEX IF EXISTS " + dropIdx)
	default: // sqlite
		_, _ = d.EasyDB().GetSqlDB().Exec("DROP INDEX IF EXISTS " + dropIdx)
	}

	items, err := db.DiffSchema(d, specs)
	if err != nil {
		t.Fatalf("DiffSchema: %v", err)
	}
	var dItems []db.DiffItem
	for _, it := range items {
		if it.Level == "D" {
			dItems = append(dItems, it)
		} else {
			t.Errorf("删单索引后不应有其它级别差异: %+v", it)
		}
	}
	if len(dItems) != 1 || dItems[0].Object != dropIdx {
		t.Fatalf("应恰好产出 1 个 D 级项（%s），got: %+v", dropIdx, dItems)
	}
	sqlText, err := db.GenerateSQL(items, specs, d)
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	// 关键断言：只生成缺失索引的单条语句，不整份重放（expires_at 索引不得出现）
	if !strings.Contains(sqlText, dropIdx) {
		t.Fatalf("生成 SQL 应含缺失索引 %s:\n%s", dropIdx, sqlText)
	}
	if strings.Contains(sqlText, "idx_ip_blacklist_expires_at") {
		t.Fatalf("生成 SQL 不应重放未缺失索引:\n%s", sqlText)
	}
	execAll(t, d, sqlText)

	items, err = db.DiffSchema(d, specs)
	if err != nil {
		t.Fatalf("复核 DiffSchema: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("补索引后应零差异，got: %+v", items)
	}
}

// runAcceptanceCatalog 验收 3：catalog 自增/默认值/可空识别（含 pg pg_node_tree LIKE 回归）。
func runAcceptanceCatalog(t *testing.T, driver, dsn string) {
	t.Helper()
	d, err := db.Open(driver, dsn)
	if err != nil {
		t.Fatalf("Open(%s) err: %v", driver, err)
	}
	const table = "schema_sync_acceptance_t"
	t.Cleanup(func() {
		_, _ = d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS " + table)
		_ = d.Close()
	})
	_, _ = d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS " + table)

	ddl := map[string]string{
		"mysql":    "CREATE TABLE " + table + " (id BIGINT AUTO_INCREMENT PRIMARY KEY, ip VARCHAR(45) NOT NULL DEFAULT '', hits INT NOT NULL DEFAULT 0)",
		"postgres": "CREATE TABLE " + table + " (id BIGSERIAL PRIMARY KEY, ip TEXT NOT NULL DEFAULT '', hits INT NOT NULL DEFAULT 0)",
		"sqlite":   "CREATE TABLE " + table + " (id INTEGER PRIMARY KEY AUTOINCREMENT, ip TEXT NOT NULL DEFAULT '', hits INTEGER NOT NULL DEFAULT 0)",
	}[driver]
	if _, err := d.EasyDB().GetSqlDB().Exec(ddl); err != nil {
		t.Fatalf("建验收表: %v", err)
	}

	cols, err := d.CatalogColumns(table)
	if err != nil {
		t.Fatalf("CatalogColumns（pg adbin::text 回归点）: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("应查回 3 列, got %d: %+v", len(cols), cols)
	}
	byName := map[string]db.CatalogColumn{}
	for _, c := range cols {
		byName[c.Name] = c
	}
	idCol := byName["id"]
	if driver == "sqlite" {
		if idCol.Extra == "" {
			t.Errorf("sqlite 自增列应能识别（pk/自动增量），got: %+v", idCol)
		}
	} else if idCol.Extra != "auto_increment" {
		t.Errorf("%s 自增列 extra 应为 auto_increment，got %q (%+v)", driver, idCol.Extra, idCol)
	}
	if byName["hits"].Default == nil {
		t.Errorf("hits 列默认值应非 NULL: %+v", byName["hits"])
	}
	if byName["ip"].Nullable != "NO" {
		t.Errorf("ip 列应 NOT NULL: %+v", byName["ip"])
	}
}

func TestSchemaSyncAcceptancePostgres(t *testing.T) {
	lockSharedDevDB(t) // 每个 Test 只加锁一次（锁到 Test 结束释放，Test 内多次加锁会同进程自阻塞）
	dsn := pgDSN()
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过")
	}
	runAcceptanceFreshDB(t, "postgres", dsn)
	runAcceptanceSingleIndex(t, "postgres", dsn)
	runAcceptanceCatalog(t, "postgres", dsn)
}

func TestSchemaSyncAcceptanceMySQL(t *testing.T) {
	lockSharedDevDB(t) // 每个 Test 只加锁一次（锁到 Test 结束释放，Test 内多次加锁会同进程自阻塞）
	dsn := mysqlDSN()
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过")
	}
	runAcceptanceFreshDB(t, "mysql", dsn)
	runAcceptanceSingleIndex(t, "mysql", dsn)
	runAcceptanceCatalog(t, "mysql", dsn)
}
