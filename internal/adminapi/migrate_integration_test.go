//go:build integration

// 数据迁移跨方言集成测试（真实 MySQL / PostgreSQL）：SQLite→MySQL、MySQL→PostgreSQL
// 的结构对齐 + 数据迁移全链路，校验行数一致、抽样字段值一致、自增序列重置生效、
// 宽表大批次经占位符预算切子批不报错。
//
// 通过环境变量门控：MYSQL_TEST_DSN / PG_TEST_DSN 非空才实际连接，否则跳过。运行：
//
//	MYSQL_TEST_DSN="<USER>:<PASSWORD>@tcp(<HOST>:3306)/<DB>" \
//	PG_TEST_DSN="postgres://<USER>:<PASSWORD>@<HOST>:5432/<DB>?sslmode=disable" \
//	go test -tags integration -run TestMigrateCrossDialect ./internal/adminapi/
//
// 注意：测试只使用带 _migtest 后缀的临时表，结束即 DROP，不触碰库内既有表。
package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iotames/easydb/dsn"

	"rocksys/internal/db"
	"rocksys/internal/taskcenter"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// crossDialectSource 源结构（SQLite 侧建表，跨方言同名同义）。
const crossSourceDDL = `CREATE TABLE migtest_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL,
    score INTEGER NOT NULL DEFAULT 0,
    ratio REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL
)`

// newCrossServer 构造跨方言测试服务器：CONF_DIR 指向临时目录，表清单指向 migtest_users
// （用 SQLite 脚本目录不可行——脚本仅 sqlite 方言；故本测试直接驱动执行器内核，
// 结构对齐用同构 DDL 在两侧手动建表，避免依赖不存在于内嵌脚本清单的临时表）。
func newCrossServer(t *testing.T) *AdminServer {
	t.Helper()
	s := New("127.0.0.1:19527", nil, nil, nil)
	dir := t.TempDir()
	s.confDir = &dir
	return s
}

// addDsn 注册数据源并返回 Code（走端点，覆盖校验链）。
func addDsn(t *testing.T, s *AdminServer, name, driver, dsnStr string) string {
	t.Helper()
	w := doJSON(s, "POST", PathDsn, fmt.Sprintf(`{"name":%q,"driver":%q,"dsn":%q}`, name, driver, dsnStr))
	if w.Code != 200 {
		t.Fatalf("注册数据源 %s 失败: %d %s", name, w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Code == "" {
		t.Fatalf("注册数据源响应异常: %s", w.Body.String())
	}
	return resp.Code
}

// buildCrossSource 建 SQLite 源库并灌 rows 行（含类型多样：文本/整数/浮点/时间）。
func buildCrossSource(t *testing.T, rows int) *db.DB {
	t.Helper()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "cross-src.db"))
	src, err := db.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开 SQLite 源库: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	if _, err := src.EasyDB().GetSqlDB().Exec(crossSourceDDL); err != nil {
		t.Fatalf("源库建表: %v", err)
	}
	tx, err := src.EasyDB().GetSqlDB().Begin()
	if err != nil {
		t.Fatalf("源库事务: %v", err)
	}
	stmt, err := tx.Prepare("INSERT INTO migtest_users (username, score, ratio, created_at) VALUES (?, ?, ?, ?)")
	if err != nil {
		t.Fatalf("源库 prepare: %v", err)
	}
	for i := 1; i <= rows; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("user%d", i), i*3, float64(i)/2, "2026-09-15 12:00:00"); err != nil {
			t.Fatalf("源库灌数 #%d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("源库提交: %v", err)
	}
	return src
}

// execOn 在指定连接执行 SQL（建表/清理）。
func execOn(t *testing.T, d *db.DB, sqlText string) {
	t.Helper()
	if _, err := d.EasyDB().GetSqlDB().Exec(sqlText); err != nil {
		t.Fatalf("执行 %q: %v", sqlText, err)
	}
}

// migrateThrough 直接驱动迁移内核（跨方言目标不在内嵌脚本表清单内，故不走 /migrate/start
// 的表清单交集校验，而直接以 migrateTable 内核验证搬运语义）。
func migrateThrough(t *testing.T, s *AdminServer, src, tgt *db.DB, table string, batch int, mode string) int64 {
	t.Helper()
	ds := dsn.DataSource{Code: "target", Name: "target", DriverName: tgt.Driver()}
	params := migrateParams{source: "self", targetDS: ds, tables: []string{table},
		batch: batch, mode: mode, selfDB: src}
	st := &migrateRunState{tables: []*tableProgress{{Table: table, Status: tablePending}}}
	rows, err := s.migrateTable(context.Background(), src, tgt, table, params, st, func(*taskcenter.Progress) {})
	if err != nil {
		t.Fatalf("迁移 %s → %s 失败: %v", src.Driver(), tgt.Driver(), err)
	}
	return rows
}

// countRowsX 数行数（方言无关）。
func countRowsX(t *testing.T, d *db.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := d.EasyDB().GetSqlDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestMigrateCrossDialect SQLite→MySQL 与 MySQL→PostgreSQL 双链路迁移。
func TestMigrateCrossDialect(t *testing.T) {
	mysqlDSN := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	pgDSN := strings.TrimSpace(os.Getenv("PG_TEST_DSN"))
	if mysqlDSN == "" || pgDSN == "" {
		t.Skip("未设置 MYSQL_TEST_DSN / PG_TEST_DSN，跳过跨方言集成测试")
	}
	s := newCrossServer(t)
	const rows = 2500

	// ① SQLite → MySQL
	src := buildCrossSource(t, rows)
	mysqlDB, err := db.Open("mysql", mysqlDSN)
	if err != nil {
		t.Fatalf("打开 MySQL: %v", err)
	}
	t.Cleanup(func() { _ = mysqlDB.Close() })
	execOn(t, mysqlDB, "DROP TABLE IF EXISTS migtest_users")
	execOn(t, mysqlDB, `CREATE TABLE migtest_users (
        id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
        username VARCHAR(64) NOT NULL,
        score INT NOT NULL DEFAULT 0,
        ratio DOUBLE NOT NULL DEFAULT 0,
        created_at DATETIME NOT NULL
    )`)
	t.Cleanup(func() { _, _ = mysqlDB.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS migtest_users") })

	// 宽表（33 列 × 2000 行）同链路验证占位符预算切子批在 MySQL 侧同样生效。
	wideCols := make([]string, 0, 33)
	for i := 1; i <= 33; i++ {
		wideCols = append(wideCols, fmt.Sprintf("c%02d TEXT", i))
	}
	wideSQLite := "CREATE TABLE migtest_wide (" + strings.Join(wideCols, ", ") + ")"
	if _, err := src.EasyDB().GetSqlDB().Exec(wideSQLite); err != nil {
		t.Fatalf("源库建宽表: %v", err)
	}
	tx, _ := src.EasyDB().GetSqlDB().Begin()
	stmt, err := tx.Prepare("INSERT INTO migtest_wide VALUES (" + strings.TrimSuffix(strings.Repeat("?,", 33), ",") + ")")
	if err != nil {
		t.Fatalf("宽表 prepare: %v", err)
	}
	for r := 1; r <= 2000; r++ {
		args := make([]any, 0, 33)
		for i := 1; i <= 33; i++ {
			args = append(args, fmt.Sprintf("v%d-%d", r, i))
		}
		if _, err := stmt.Exec(args...); err != nil {
			t.Fatalf("宽表灌数 #%d: %v", r, err)
		}
	}
	_ = stmt.Close()
	_ = tx.Commit()
	execOn(t, mysqlDB, "DROP TABLE IF EXISTS migtest_wide")
	execOn(t, mysqlDB, "CREATE TABLE migtest_wide ("+strings.Join(wideCols, ", ")+")")
	t.Cleanup(func() { _, _ = mysqlDB.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS migtest_wide") })

	if n := migrateThrough(t, s, src, mysqlDB, "migtest_users", 1000, migrateModeReplace); n != rows {
		t.Fatalf("SQLite→MySQL 迁移行数 = %d, want %d", n, rows)
	}
	if n := countRowsX(t, mysqlDB, "migtest_users"); n != rows {
		t.Fatalf("MySQL 目标行数 = %d, want %d", n, rows)
	}
	// 抽样字段值一致（文本/整数/浮点/时间四类）。
	var username string
	var score int
	var ratio float64
	var createdAt string
	if err := mysqlDB.EasyDB().GetSqlDB().QueryRow(
		"SELECT username, score, ratio, created_at FROM migtest_users ORDER BY id LIMIT 1").
		Scan(&username, &score, &ratio, &createdAt); err != nil {
		t.Fatalf("MySQL 抽样: %v", err)
	}
	if username != "user1" || score != 3 || ratio != 0.5 || !strings.HasPrefix(createdAt, "2026-09-15") {
		t.Fatalf("MySQL 抽样值不符: %s %d %v %s", username, score, ratio, createdAt)
	}
	// 自增序列重置：迁移后补写新记录，主键应为 max(id)+1。
	if _, err := mysqlDB.EasyDB().GetSqlDB().Exec(
		"INSERT INTO migtest_users (username, score, ratio, created_at) VALUES ('after','1','1','2026-09-15 13:00:00')"); err != nil {
		t.Fatalf("MySQL 迁移后补写应不冲突: %v", err)
	}
	var newID int64
	if err := mysqlDB.EasyDB().GetSqlDB().QueryRow(
		"SELECT id FROM migtest_users WHERE username='after'").Scan(&newID); err != nil {
		t.Fatalf("MySQL 取新记录主键: %v", err)
	}
	if newID <= rows {
		t.Fatalf("MySQL 自增序列未重置: new id = %d, want > %d", newID, rows)
	}
	// skip 模式重跑：冲突行跳过，不报错（幂等补数语义）。
	if n := migrateThrough(t, s, src, mysqlDB, "migtest_users", 1000, migrateModeSkip); n != rows {
		t.Fatalf("skip 重跑迁移行数 = %d, want %d", n, rows)
	}
	// 宽表大批次：33 列 × 1000 行 = 33000 占位符，预算 30000 切子批后应完整迁完。
	if n := migrateThrough(t, s, src, mysqlDB, "migtest_wide", 1000, migrateModeReplace); n != 2000 {
		t.Fatalf("宽表迁移行数 = %d, want 2000", n)
	}
	if n := countRowsX(t, mysqlDB, "migtest_wide"); n != 2000 {
		t.Fatalf("MySQL 宽表行数 = %d, want 2000", n)
	}

	// ② MySQL → PostgreSQL（源改为 MySQL 侧刚迁完的数据）
	pgDB, err := db.Open("postgres", pgDSN)
	if err != nil {
		t.Fatalf("打开 PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = pgDB.Close() })
	execOn(t, pgDB, "DROP TABLE IF EXISTS migtest_users")
	execOn(t, pgDB, `CREATE TABLE migtest_users (
        id SERIAL PRIMARY KEY,
        username VARCHAR(64) NOT NULL,
        score INTEGER NOT NULL DEFAULT 0,
        ratio DOUBLE PRECISION NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL
    )`)
	t.Cleanup(func() { _, _ = pgDB.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS migtest_users") })

	wantRows := countRowsX(t, mysqlDB, "migtest_users")
	if n := migrateThrough(t, s, mysqlDB, pgDB, "migtest_users", 1000, migrateModeReplace); n != wantRows {
		t.Fatalf("MySQL→PG 迁移行数 = %d, want %d", n, wantRows)
	}
	if n := countRowsX(t, pgDB, "migtest_users"); n != wantRows {
		t.Fatalf("PG 目标行数 = %d, want %d", n, wantRows)
	}
	var pgName string
	var pgScore int
	var pgRatio float64
	if err := pgDB.EasyDB().GetSqlDB().QueryRow(
		"SELECT username, score, ratio FROM migtest_users ORDER BY id LIMIT 1").
		Scan(&pgName, &pgScore, &pgRatio); err != nil {
		t.Fatalf("PG 抽样: %v", err)
	}
	if pgName != "user1" || pgScore != 3 || pgRatio != 0.5 {
		t.Fatalf("PG 抽样值不符: %s %d %v", pgName, pgScore, pgRatio)
	}
	// PG 序列重置（setval）：补写新记录不冲突。
	if _, err := pgDB.EasyDB().GetSqlDB().Exec(
		"INSERT INTO migtest_users (username, score, ratio, created_at) VALUES ('after-pg',1,1,'2026-09-15 14:00:00')"); err != nil {
		t.Fatalf("PG 迁移后补写应不冲突: %v", err)
	}
	var pgNewID int64
	if err := pgDB.EasyDB().GetSqlDB().QueryRow(
		"SELECT id FROM migtest_users WHERE username='after-pg'").Scan(&pgNewID); err != nil {
		t.Fatalf("PG 取新记录主键: %v", err)
	}
	if pgNewID <= wantRows {
		t.Fatalf("PG 自增序列未重置: new id = %d, want > %d", pgNewID, wantRows)
	}
}

// TestMigrateSchemaAlignCrossDialect 结构对齐真库验收（MySQL / PostgreSQL）：以系统内嵌
// 期望结构为目标库建全套业务表（对齐前 diff 非空 → 后台任务执行 DDL → 复检 diff 为空）。
// 目标库为测试内自建的临时数据库（自建自清），避免既有库残留表干扰「零差异」断言。
func TestMigrateSchemaAlignCrossDialect(t *testing.T) {
	mysqlDSN := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	pgDSN := strings.TrimSpace(os.Getenv("PG_TEST_DSN"))
	if mysqlDSN == "" && pgDSN == "" {
		t.Skip("未设置 MYSQL_TEST_DSN / PG_TEST_DSN，跳过结构对齐集成测试")
	}
	s := newCrossServer(t)
	s.tableSpecs = buildCrossTableSpecs()

	type alignCase struct {
		driver string
		setup  func(t *testing.T) (string, func())
	}
	cases := map[string]alignCase{}
	if mysqlDSN != "" {
		cases["mysql"] = alignCase{driver: "mysql", setup: createTempMySQL}
	}
	if pgDSN != "" {
		cases["postgres"] = alignCase{driver: "postgres", setup: createTempPostgres}
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			targetDSN, cleanup := tc.setup(t)
			defer cleanup()
			code := addDsn(t, s, "align-"+name, tc.driver, targetDSN)

			// ① 差异预览：空库应报缺表且有 DDL。
			w := doJSON(s, "GET", PathMigrateSchema+"?code="+code, "")
			if w.Code != 200 {
				t.Fatalf("diff 预览失败: %d %s", w.Code, w.Body.String())
			}
			var diff struct {
				Items []db.DiffItem `json:"items"`
				SQL   string        `json:"sql"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil {
				t.Fatalf("diff 解析: %v", err)
			}
			if len(diff.Items) == 0 || !strings.Contains(strings.ToUpper(diff.SQL), "CREATE TABLE") {
				t.Fatalf("空目标库应有建表差异: items=%d", len(diff.Items))
			}

			// ② 执行对齐（后台任务）→ 等终态。
			w = doJSON(s, "POST", PathMigrateSchemaApply+"?code="+code, "")
			var applyResp struct {
				TaskID string `json:"task_id"`
				Noop   bool   `json:"noop"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &applyResp); err != nil || applyResp.TaskID == "" {
				t.Fatalf("apply 提交响应异常: %s", w.Body.String())
			}
			waitTaskX(t, s, applyResp.TaskID)

			// ③ 复检：零差异（目标库结构与运行库表同步结果一致）。
			w = doJSON(s, "GET", PathMigrateSchema+"?code="+code, "")
			var diff2 struct {
				Items []db.DiffItem `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &diff2); err != nil || len(diff2.Items) != 0 {
				t.Fatalf("%s 对齐后应零差异: %s", tc.driver, w.Body.String())
			}
		})
	}
}

// buildCrossTableSpecs 期望结构表清单（与 cmd/rocksys 装配同源，此处按内嵌脚本重建）。
func buildCrossTableSpecs() []db.TableSpec {
	return []db.TableSpec{
		{Table: "admin_users", CreateScript: "admin_users_create_table.sql"},
		{Table: "ip_blacklist", CreateScript: "ip_blacklist_create_table.sql", IndexScript: "ip_blacklist_create_index.sql"},
		{Table: "ip_whitelist", CreateScript: "ip_whitelist_create_table.sql", IndexScript: "ip_whitelist_create_index.sql"},
		{Table: "attack_archive", CreateScript: "attack_archive_create_table.sql", IndexScript: "attack_archive_create_index.sql"},
		{Table: "shield_event", CreateScript: "shield_event_create_table.sql", IndexScript: "shield_event_create_index.sql"},
		{Table: "access_log", CreateScript: "access_log_create_table.sql", IndexScript: "access_log_create_index.sql"},
		{Table: "sql_exec_log", CreateScript: "sql_exec_log_create_table.sql", IndexScript: "sql_exec_log_create_index.sql"},
		{Table: "outbox", CreateScript: "mq_create_table.sql", IndexScript: "mq_create_index.sql"},
		{Table: "geoip_list", CreateScript: "geoip_list_create_table.sql", IndexScript: "geoip_list_create_index.sql"},
		{Table: "schedule_list", CreateScript: "schedule_list_create_table.sql"},
	}
}

// waitTaskX 轮询任务至终态并断言 done。
func waitTaskX(t *testing.T, s *AdminServer, id string) taskcenter.Task {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := s.tasks.Get(id); ok && task.Status.Terminal() {
			if task.Status != taskcenter.StatusDone {
				t.Fatalf("任务 %s 终态 = %s result=%q progress=%v", id, task.Status, task.Result, task.Progress)
			}
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未在限时内终态", id)
	return taskcenter.Task{}
}

// createTempMySQL 在 MySQL 测试实例上建临时库并返回其 DSN（cleanup 时 DROP）。
func createTempMySQL(t *testing.T) (string, func()) {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	server, _, ok := strings.Cut(base, ")")
	if !ok {
		t.Skipf("MYSQL_TEST_DSN 形态不支持自动建库（需 host(...)/db 形态）：%s", base)
	}
	server += ")"
	admin, err := db.Open("mysql", server+"/")
	if err != nil {
		t.Fatalf("连接 MySQL 实例失败: %v", err)
	}
	const dbName = "rocksys_align_it"
	if _, err := admin.EasyDB().GetSqlDB().Exec("DROP DATABASE IF EXISTS " + dbName); err != nil {
		t.Fatalf("清理旧临时库: %v", err)
	}
	if _, err := admin.EasyDB().GetSqlDB().Exec("CREATE DATABASE " + dbName); err != nil {
		t.Fatalf("创建临时库: %v", err)
	}
	_ = admin.Close()
	return server + "/" + dbName, func() {
		a, err := db.Open("mysql", server+"/")
		if err == nil {
			_, _ = a.EasyDB().GetSqlDB().Exec("DROP DATABASE IF EXISTS " + dbName)
			_ = a.Close()
		}
	}
}

// createTempPostgres 在 PG 测试实例上建临时库并返回其 DSN（cleanup 时 DROP）。
func createTempPostgres(t *testing.T) (string, func()) {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("PG_TEST_DSN"))
	idx := strings.LastIndex(base, "/")
	if idx < 0 {
		t.Skipf("PG_TEST_DSN 形态不支持自动建库：%s", base)
	}
	prefix := base[:idx+1]
	query := ""
	// sslmode 等参数在库名之后（postgres://host:5432/db?sslmode=disable）：从尾部截取，
	// 拼装临时库 DSN 时必须保留，否则 lib/pq 回落默认 sslmode=require 而连接被拒。
	if q := strings.Index(base[idx+1:], "?"); q >= 0 {
		query = base[idx+1+q:]
	}
	admin, err := db.Open("postgres", prefix+"postgres"+query)
	if err != nil {
		t.Fatalf("连接 PG 实例失败: %v", err)
	}
	const dbName = "rocksys_align_it"
	if _, err := admin.EasyDB().GetSqlDB().Exec("DROP DATABASE IF EXISTS " + dbName); err != nil {
		t.Fatalf("清理旧临时库: %v", err)
	}
	if _, err := admin.EasyDB().GetSqlDB().Exec("CREATE DATABASE " + dbName); err != nil {
		t.Fatalf("创建临时库: %v", err)
	}
	_ = admin.Close()
	return prefix + dbName + query, func() {
		a, err := db.Open("postgres", prefix+"postgres"+query)
		if err == nil {
			_, _ = a.EasyDB().GetSqlDB().Exec("DROP DATABASE IF EXISTS " + dbName)
			_ = a.Close()
		}
	}
}

// TestMigrateCrossDialectCancelled 迁移中途取消：已写入行保留、任务可重跑续接（跨方言真库）。
func TestMigrateCrossDialectCancelled(t *testing.T) {
	mysqlDSN := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if mysqlDSN == "" {
		t.Skip("未设置 MYSQL_TEST_DSN，跳过跨方言取消集成测试")
	}
	s := newCrossServer(t)
	src := buildCrossSource(t, 20000)
	mysqlDB, err := db.Open("mysql", mysqlDSN)
	if err != nil {
		t.Fatalf("打开 MySQL: %v", err)
	}
	t.Cleanup(func() { _ = mysqlDB.Close() })
	execOn(t, mysqlDB, "DROP TABLE IF EXISTS migtest_users")
	execOn(t, mysqlDB, `CREATE TABLE migtest_users (
        id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
        username VARCHAR(64) NOT NULL,
        score INT NOT NULL DEFAULT 0,
        ratio DOUBLE NOT NULL DEFAULT 0,
        created_at DATETIME NOT NULL
    )`)
	t.Cleanup(func() { _, _ = mysqlDB.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS migtest_users") })

	ds := dsn.DataSource{Code: "t", Name: "t", DriverName: "mysql"}
	params := migrateParams{source: "self", targetDS: ds, tables: []string{"migtest_users"},
		batch: 200, mode: migrateModeReplace, selfDB: src}
	st := &migrateRunState{tables: []*tableProgress{{Table: "migtest_users", Status: tablePending}}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.migrateTable(ctx, src, mysqlDB, "migtest_users", params, st, func(*taskcenter.Progress) {})
		done <- err
	}()
	// 等首批落库后取消（批边界生效）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countRowsX(t, mysqlDB, "migtest_users") > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Log("迁移在取消送达前已完成（数据量小），取消语义由批边界单测覆盖")
	}
	partial := countRowsX(t, mysqlDB, "migtest_users")
	if partial == 0 || partial > 20000 {
		t.Fatalf("取消后已写入行数异常: %d", partial)
	}
	// replace 重跑幂等：重来一遍后全量一致。
	if n := migrateThrough(t, s, src, mysqlDB, "migtest_users", 1000, migrateModeReplace); n != 20000 {
		t.Fatalf("重跑行数 = %d, want 20000", n)
	}
	if n := countRowsX(t, mysqlDB, "migtest_users"); n != 20000 {
		t.Fatalf("重跑后行数 = %d, want 20000", n)
	}
}
