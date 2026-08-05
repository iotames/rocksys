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
	if _, err := Open("oracle", "", ""); err == nil {
		t.Error("oracle 未内嵌 sql/oracle/ 目录，Open 应报错")
	}
}

// TestOpenAndSQL 临时 sqlite 文件打开成功，脚本源可读 mq 全量脚本。
func TestOpenAndSQL(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")

	d, err := Open("sqlite", dsn, "sql")
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

// TestExternalSQLDirOverride SQL_DIR 外置目录优先：覆盖内容生效、缺文件回退内嵌、
// 空文件回退内嵌（ScriptDir 逐级加载语义，见 internal/hotswap/script.go）。
func TestExternalSQLDirOverride(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, content string) {
		p := filepath.Join(dir, "sql", "sqlite", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	// 1) 外置覆盖 mq_insert.sql → SQL() 返回外置内容
	mk("mq_insert.sql", "INSERT INTO external_table (a) VALUES (1)")
	d, err := Open("sqlite", filepath.Join(t.TempDir(), "x.db"), filepath.Join(dir, "sql"))
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
// （兑现"SQL_DIR 外置兜底"承诺）；两者皆无时拒绝。
func TestOpenExternalDialectDirFallback(t *testing.T) {
	// 注册假驱动 "oracletest"，模拟"驱动已注册但内嵌缺方言目录"（真实驱动均有内嵌目录）。
	sql.Register("oracletest", fakeDriver{})
	dir := t.TempDir()
	// 外置提供 sql/oracletest/ 目录（内嵌无此方言）→ 放行
	if err := os.MkdirAll(filepath.Join(dir, "oracletest"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := Open("oracletest", filepath.Join(t.TempDir(), "x.db"), dir)
	if err != nil {
		t.Fatalf("外置目录提供 sql/oracletest/ 时应放行: %v", err)
	}
	_ = d.Close()

	// 外置亦无 oracletest → 拒绝
	if _, err := Open("oracletest", filepath.Join(t.TempDir(), "x.db"), filepath.Join(t.TempDir(), "nonexistent")); err == nil {
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
