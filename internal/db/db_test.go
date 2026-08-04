package db

import (
	"path/filepath"
	"strings"
	"testing"

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

// TestEmbeddedSQLSourceMissing 切换数据库但缺脚本时 SQL() 应报错。
func TestEmbeddedSQLSourceMissing(t *testing.T) {
	src, err := EmbeddedSQLSource("postgres")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(postgres) err: %v", err)
	}
	if _, err := src.SQL("mq_insert.sql"); err == nil {
		t.Error("sql/postgres 下无 mq_insert.sql，SQL() 应报错")
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
