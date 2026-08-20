package db

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlfiles "rocksys"
	"rocksys/internal/hotswap"

	_ "modernc.org/sqlite"
)

// TestEmbeddedSQLSourceSQLite 内嵌 sqlite 脚本可读且含预期片段。
func TestEmbeddedSQLSourceSQLite(t *testing.T) {
	src, err := EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(sqlite) err: %v", err)
	}
	txt, err := src.SQL("mq_insert.sql")
	if err != nil {
		t.Fatalf("SQL(mq_insert.sql) err: %v", err)
	}
	if !strings.Contains(txt, "INSERT INTO") || !strings.Contains(txt, "{table}") {
		t.Errorf("mq_insert.sql 内容异常: %q", txt)
	}
}

// TestEmbeddedSQLSourceMissing 切换数据库但缺脚本时 SQL() 应报错（缺脚本即报错铁律）。
func TestEmbeddedSQLSourceMissing(t *testing.T) {
	src, err := EmbeddedSQLSource("postgres")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(postgres) err: %v", err)
	}
	if _, err := src.SQL("nonexistent_script.sql"); err == nil {
		t.Error("sql/postgres 下无 nonexistent_script.sql，SQL() 应报错")
	}
}

// TestOpenUnsupportedDriver 驱动未内嵌脚本目录时，Open 阶段即报错（不静默降级）。
func TestOpenUnsupportedDriver(t *testing.T) {
	if _, err := Open("oracle", ""); err == nil {
		t.Error("oracle 未内嵌 sql/oracle/ 目录，Open 应报错")
	}
}

// TestOpenWithHub 注入 ScriptHub 统一内容中枢（实现见 internal/hotswap/hub.go）后：
// sql/ 子目录注册进中枢，SQL() 经统一缓存读取（外挂优先、内嵌兜底）；
// 同一 hub 重复 Open 报错（sql 子目录重复注册 = 装配缺陷，尽早暴露）。
func TestOpenWithHub(t *testing.T) {
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	ext := t.TempDir()
	hotswap.SetHotScriptsDir(ext)

	// 外挂覆写 sql/sqlite/mq_insert.sql：内容与内嵌不同，断言"外挂优先"
	dir := filepath.Join(ext, scriptSubDir, "sqlite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	extTxt := "INSERT INTO outbox_hub_test (payload) VALUES (?);"
	if err := os.WriteFile(filepath.Join(dir, "mq_insert.sql"), []byte(extTxt), 0o644); err != nil {
		t.Fatal(err)
	}

	hub := hotswap.NewScriptHub(0)
	d, err := Open("sqlite", filepath.Join(t.TempDir(), "hub.db"), hub)
	if err != nil {
		t.Fatalf("Open(..., hub) err: %v", err)
	}
	defer d.Close()

	txt, err := d.SQL("mq_insert.sql")
	if err != nil {
		t.Fatalf("SQL(mq_insert.sql) err: %v", err)
	}
	if !strings.Contains(txt, "outbox_hub_test") {
		t.Fatalf("注入 hub 后 SQL() 应经统一缓存外挂优先，got %q", txt)
	}

	// 同一 hub 重复 Open → sql 子目录重复注册报错（装配缺陷）
	if _, err := Open("sqlite", filepath.Join(t.TempDir(), "hub2.db"), hub); err == nil {
		t.Fatal("同一 hub 重复 Open 应报错（sql 子目录重复注册）")
	}
}

// TestOpenAndSQL 临时 sqlite 文件打开成功，脚本源可读 mq 全量脚本。
func TestOpenAndSQL(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")

	d, err := Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("Open err: %v", err)
	}
	defer d.Close()
	if d.Driver() != "sqlite" {
		t.Errorf("Driver=%q，want sqlite", d.Driver())
	}

	for _, name := range []string{
		"mq_create_table.sql",
		"mq_create_index.sql",
		"mq_insert.sql",
		"mq_fetch_pending.sql",
		"mq_mark_done.sql",
		"mq_mark_failed.sql",
		"mq_get_retry_count.sql",
		"mq_mark_dead.sql",
	} {
		if _, err := d.SQL(name); err != nil {
			t.Errorf("SQL(%s) err: %v", name, err)
		}
	}
}

// TestScriptParity 三方言（sqlite/mysql/postgres）脚本文件集必须完全一致
// （数据库铁律④：sqlite/mysql/postgres 三方言齐平，缺脚本即报错）。
func TestScriptParity(t *testing.T) {
	sub, err := fs.Sub(sqlfiles.FS, "sql")
	if err != nil {
		t.Fatalf("fs.Sub(sql) err: %v", err)
	}
	names := func(dialect string) []string {
		entries, err := fs.ReadDir(sub, dialect)
		if err != nil {
			t.Fatalf("ReadDir(%s) err: %v", dialect, err)
		}
		var out []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				out = append(out, e.Name())
			}
		}
		return out
	}
	got := map[string][]string{}
	for _, d := range []string{"sqlite", "mysql", "postgres"} {
		got[d] = names(d)
	}

	// 与 sqlite（基准方言）对比，双方差集合必须为空。
	for _, d := range []string{"mysql", "postgres"} {
		missing, extra := diffScripts(got["sqlite"], got[d])
		if len(missing) > 0 {
			t.Errorf("sql/%s 缺少脚本（sqlite 已有）：%v", d, missing)
		}
		if len(extra) > 0 {
			t.Errorf("sql/%s 多余脚本（sqlite 没有）：%v", d, extra)
		}
	}
}

// TestSplitSQLStatements 拆分器剥离空行与 "--" 注释行，保留语句行。
func TestSplitSQLStatements(t *testing.T) {
	in := "-- 注释行\n\nCREATE INDEX a ON t(id)\n-- 又一注释\nCREATE INDEX b ON t(name)\n\n"
	got := SplitSQLStatements(in)
	want := []string{"CREATE INDEX a ON t(id)", "CREATE INDEX b ON t(name)"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stmt[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// TestExternalSQLDirOverride HOT_SCRIPTS_DIR/sql 外挂目录优先：覆盖内容生效、缺文件回退内嵌、
// 空文件回退内嵌（ScriptDir 逐级加载语义，见 internal/hotswap/script.go）。
// ★ 统一收敛：外挂 SQL 覆写目录固定为 HOT_SCRIPTS_DIR/sql（测试经 hotswap.SetHotScriptsDir 隔离外挂根）。
func TestExternalSQLDirOverride(t *testing.T) {
	// 注入临时外挂根，隔离测试；结束恢复默认 hotscripts。
	origExt := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(origExt) })
	ext := t.TempDir()
	hotswap.SetHotScriptsDir(ext)

	mk := func(name, content string) {
		p := filepath.Join(ext, scriptSubDir, "sqlite", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	// 1) 外置覆盖 mq_insert.sql → SQL() 返回外置内容
	mk("mq_insert.sql", "INSERT INTO external_table (a) VALUES (1)")
	d, err := Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open err: %v", err)
	}
	defer d.Close()
	txt, err := d.SQL("mq_insert.sql")
	if err != nil {
		t.Fatalf("SQL(mq_insert.sql) err: %v", err)
	}
	if !strings.Contains(txt, "external_table") {
		t.Errorf("外置覆盖未生效: %q", txt)
	}

	// 2) 外置缺文件 → 回退内嵌
	txt, err = d.SQL("mq_fetch_pending.sql")
	if err != nil {
		t.Fatalf("SQL(mq_fetch_pending.sql) err: %v", err)
	}
	if !strings.Contains(txt, "SELECT") {
		t.Errorf("缺文件应回退内嵌，got %q", txt)
	}

	// 3) 外置文件存在但为空 → 回退内嵌
	mk("mq_fetch_pending.sql", "")
	txt, err = d.SQL("mq_fetch_pending.sql")
	if err != nil {
		t.Fatalf("SQL(mq_fetch_pending.sql) err: %v", err)
	}
	if !strings.Contains(txt, "SELECT") {
		t.Errorf("空文件应回退内嵌，got %q", txt)
	}
}

// TestOpenExternalDialectDirFallback 内嵌缺方言目录但外置提供时，Open 应放行
// （兑现"外挂 sql/ 兜底"承诺）；两者皆无时拒绝。
func TestOpenExternalDialectDirFallback(t *testing.T) {
	// 注册假驱动 "oracletest"，模拟"驱动已注册但内嵌缺方言目录"（真实驱动均有内嵌目录）。
	sql.Register("oracletest", fakeDriver{})

	origExt := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(origExt) })

	// 外置提供 HOT_SCRIPTS_DIR/sql/oracletest/ 目录（内嵌无此方言）→ 放行
	dir := t.TempDir()
	hotswap.SetHotScriptsDir(dir)
	if err := os.MkdirAll(filepath.Join(dir, scriptSubDir, "oracletest"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := Open("oracletest", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("外置目录提供 sql/oracletest/ 时应放行: %v", err)
	}
	_ = d.Close()

	// 外置亦无 oracletest → 拒绝
	hotswap.SetHotScriptsDir(filepath.Join(t.TempDir(), "nonexistent"))
	if _, err := Open("oracletest", filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Error("内嵌与外置均无 sql/oracletest/ 时 Open 应拒绝")
	}
}

// fakeDriver 最小假驱动：仅用于测试"驱动已注册但内嵌缺脚本目录"的 Open 放行路径。
type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) { return fakeConn{}, nil }

type fakeConn struct{}

func (fakeConn) Prepare(query string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (fakeConn) Close() error                              { return nil }
func (fakeConn) Begin() (driver.Tx, error)                 { return nil, errors.New("unused") }

// diffScripts 返回 a 中有而 b 中没有的（missing）、b 中有而 a 中没有的（extra）。
func diffScripts(a, b []string) (missing, extra []string) {
	sa, sb := map[string]bool{}, map[string]bool{}
	for _, n := range a {
		sa[n] = true
	}
	for _, n := range b {
		sb[n] = true
	}
	for n := range sa {
		if !sb[n] {
			missing = append(missing, n)
		}
	}
	for n := range sb {
		if !sa[n] {
			extra = append(extra, n)
		}
	}
	return missing, extra
}

// ---- ensureSQLitePragma 单测（P1） ----

const wantPragmaSuffix = "?_busy_timeout=5000&_journal_mode=WAL"

// TestEnsureSQLitePragmaBarePath 裸路径 → 用 ? 连接追加。
func TestEnsureSQLitePragmaBarePath(t *testing.T) {
	got := ensureSQLitePragma("/tmp/x.db")
	want := "/tmp/x.db" + wantPragmaSuffix
	if got != want {
		t.Errorf("ensureSQLitePragma(/tmp/x.db)=%q want %q", got, want)
	}
}

// TestEnsureSQLitePragmaWithParams 已有 ? 参数（非 _ 前缀）→ 用 & 追加。
func TestEnsureSQLitePragmaWithParams(t *testing.T) {
	got := ensureSQLitePragma("/tmp/x.db?cache=shared")
	want := "/tmp/x.db?cache=shared&_busy_timeout=5000&_journal_mode=WAL"
	if got != want {
		t.Errorf("ensureSQLitePragma 带普通参数=%q want %q", got, want)
	}
}

// TestEnsureSQLitePragmaAlreadySet 已含 _ 前缀 pragma 参数 → 原样返回（尊重显式配置）。
func TestEnsureSQLitePragmaAlreadySet(t *testing.T) {
	for _, dsn := range []string{
		"/tmp/x.db?_busy_timeout=1000",
		"/tmp/x.db?_journal_mode=MEMORY",
		"/tmp/x.db?_pragma=busy_timeout(3000)",
		"/tmp/x.db?_timeout=2000",
		"/tmp/x.db?_fk=1",
	} {
		if got := ensureSQLitePragma(dsn); got != dsn {
			t.Errorf("ensureSQLitePragma(%q)=%q，已显式配置应原样返回", dsn, got)
		}
	}
}

// TestEnsureSQLitePragmaMemory 内存库 → 原样返回（补参无意义）。
func TestEnsureSQLitePragmaMemory(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:?cache=shared"} {
		if got := ensureSQLitePragma(dsn); got != dsn {
			t.Errorf("ensureSQLitePragma(%q)=%q，内存库应原样返回", dsn, got)
		}
	}
}

// TestEnsureSQLitePragmaPathWithEqAmp 文件名含 = 与 &（无 ? 分隔）→ 路径原样保留、用 ? 连接。
func TestEnsureSQLitePragmaPathWithEqAmp(t *testing.T) {
	dsn := "/tmp/x=y&z.db"
	got := ensureSQLitePragma(dsn)
	want := dsn + wantPragmaSuffix
	if got != want {
		t.Errorf("ensureSQLitePragma(%q)=%q want %q（路径不得交给 query 解析器）", dsn, got, want)
	}
}

// TestEnsureSQLitePragmaEmpty 空串 → 原样返回。
func TestEnsureSQLitePragmaEmpty(t *testing.T) {
	if got := ensureSQLitePragma(""); got != "" {
		t.Errorf("ensureSQLitePragma(\"\")=%q，空串应原样返回", got)
	}
}

// TestOpenSQLiteAutoPragma 集成：Open 后 PRAGMA journal_mode=wal、busy_timeout=5000。
func TestOpenSQLiteAutoPragma(t *testing.T) {
	d, err := Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open err: %v", err)
	}
	defer d.Close()

	query := func(pragma string, dest any) {
		t.Helper()
		if err := d.EasyDB().GetSqlDB().QueryRow("PRAGMA " + pragma).Scan(dest); err != nil {
			t.Fatalf("PRAGMA %s 查询失败: %v", pragma, err)
		}
	}
	var mode string
	query("journal_mode", &mode)
	if mode != "wal" {
		t.Errorf("journal_mode=%q，want wal（默认 DSN 已自动补参）", mode)
	}
	var bt int
	query("busy_timeout", &bt)
	if bt != sqliteBusyTimeout {
		t.Errorf("busy_timeout=%d，want %d", bt, sqliteBusyTimeout)
	}
}
