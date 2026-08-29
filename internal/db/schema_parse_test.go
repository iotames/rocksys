package db

// schema_parse_test.go：DDL 解析器表驱动单测。
// fixtures = 编译期内嵌的三方言真实脚本（经根包 sqlfiles embed FS 直读）——
// 改脚本即改测试输入，天然防解析器与脚本脱节。

import (
	"io/fs"
	"strings"
	"testing"

	sqlfiles "rocksys"
)

// readSQLFS 返回内嵌 sql/ 子树。
func readSQLFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(sqlfiles.FS, "sql")
	if err != nil {
		t.Fatalf("读取内嵌 sql/ 目录失败: %v", err)
	}
	return sub
}

// dialectScripts 收集 sql/<dialect>/ 下匹配后缀的全部脚本文本（{table} 已替换为实际表名）。
func dialectScripts(t *testing.T, dialect, suffix string) map[string]string {
	t.Helper()
	sub := readSQLFS(t)
	entries, err := fs.ReadDir(sub, dialect)
	if err != nil {
		t.Fatalf("读取内嵌 sql/%s/ 失败: %v", dialect, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		txt, err := fs.ReadFile(sub, dialect+"/"+e.Name())
		if err != nil {
			t.Fatalf("读取 sql/%s/%s 失败: %v", dialect, e.Name(), err)
		}
		out[e.Name()] = strings.ReplaceAll(string(txt), "{table}", "t")
	}
	if len(out) == 0 {
		t.Fatalf("sql/%s/ 下无匹配 %s 的脚本", dialect, suffix)
	}
	return out
}

// TestParseCreateTableRealScripts 三方言全部建表脚本解析零错误 + 列集合断言。
// 断言用超集包含（ip_blacklist 九列必须全部解析出），不断言精确相等——防后续加列（如 warn_times）破坏测试。
func TestParseCreateTableRealScripts(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		scripts := dialectScripts(t, dialect, "_create_table.sql")
		if len(scripts) < 7 {
			t.Errorf("方言 %s 建表脚本仅 %d 份（期望 ≥7）", dialect, len(scripts))
		}
		for name, ddl := range scripts {
			cols, err := ParseCreateTable(ddl)
			if err != nil {
				t.Errorf("%s/%s 解析失败: %v", dialect, name, err)
				continue
			}
			if len(cols) == 0 {
				t.Errorf("%s/%s 解析出 0 列", dialect, name)
				continue
			}
			for _, c := range cols {
				if c.Name == "" || c.Type == "" {
					t.Errorf("%s/%s 列定义缺失名称或类型: %+v", dialect, name, c)
				}
				if c.Raw == "" {
					t.Errorf("%s/%s 列 %s Raw 为空（ADD COLUMN 复用依赖原文）", dialect, name, c.Name)
				}
			}
		}
	}
}

// TestParseCreateTableIPBlacklist ip_blacklist 列集合超集断言 + 三方言自增主键识别。
func TestParseCreateTableIPBlacklist(t *testing.T) {
	want := []string{"id", "ip", "title", "block_type", "hit_count", "expires_at", "deleted_at", "created_at", "updated_at"}
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		ddl, ok := dialectScripts(t, dialect, "_create_table.sql")["ip_blacklist_create_table.sql"]
		if !ok {
			t.Fatalf("方言 %s 缺少 ip_blacklist_create_table.sql", dialect)
		}
		cols, err := ParseCreateTable(ddl)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", dialect, err)
		}
		got := map[string]ColumnDef{}
		for _, c := range cols {
			got[c.Name] = c
		}
		for _, name := range want {
			if _, ok := got[name]; !ok {
				t.Errorf("%s ip_blacklist 缺少列 %s（解析出 %d 列）", dialect, name, len(cols))
			}
		}
		id := got["id"]
		if dialect == "postgres" {
			if !id.IsAutoInc {
				t.Errorf("%s id 列应识别为自增（BIGSERIAL）", dialect)
			}
		} else if !id.IsPK || !id.IsAutoInc {
			t.Errorf("%s id 列应识别为主键自增，got IsPK=%v IsAutoInc=%v", dialect, id.IsPK, id.IsAutoInc)
		}
		// 表级约束不产出列：mysql 的 UNIQUE KEY uk_t_ip (ip) 不应解析出名为 uk_t_ip 的列
		for _, c := range cols {
			if strings.HasPrefix(c.Name, "uk_") {
				t.Errorf("%s 表级约束 %s 被误解析为列", dialect, c.Name)
			}
		}
	}
}

// TestParseColumnDetails 单列细节：NOT NULL / DEFAULT / Raw 复用与 mysql COMMENT 剥离。
func TestParseColumnDetails(t *testing.T) {
	ddl := strings.ReplaceAll(dialectScripts(t, "sqlite", "_create_table.sql")["ip_blacklist_create_table.sql"], "{table}", "t")
	cols, err := ParseCreateTable(ddl)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	byName := map[string]ColumnDef{}
	for _, c := range cols {
		byName[c.Name] = c
	}
	if c := byName["ip"]; !c.NotNull {
		t.Errorf("ip 列应 NOT NULL: %+v", c)
	}
	if c := byName["title"]; c.Default == nil || *c.Default != "''" {
		t.Errorf("title 列默认值应为 ''，got %v", c.Default)
	}
	if c := byName["expires_at"]; c.NotNull {
		t.Errorf("expires_at 列可为 NULL: %+v", c)
	}

	mysqlDDL := dialectScripts(t, "mysql", "_create_table.sql")["ip_blacklist_create_table.sql"]
	mcols, err := ParseCreateTable(mysqlDDL)
	if err != nil {
		t.Fatalf("mysql 解析失败: %v", err)
	}
	var mhit ColumnDef
	for _, c := range mcols {
		if c.Name == "hit_count" {
			mhit = c
		}
	}
	if mhit.Name == "" {
		t.Fatalf("mysql 未解析出 hit_count 列")
	}
	if mhit.Default == nil || *mhit.Default != "0" {
		t.Errorf("mysql hit_count 默认值应为 0，got %v（COMMENT '...' 不得混入默认值）", mhit.Default)
	}
	if !strings.Contains(mhit.Raw, "INT NOT NULL") {
		t.Errorf("mysql hit_count Raw 应保留列定义原文，got %q", mhit.Raw)
	}
}

// TestParseIndexNames 三方言索引脚本提取索引名（mysql 无 IF NOT EXISTS 亦须命中）。
func TestParseIndexNames(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		names := map[string]bool{}
		for _, txt := range dialectScripts(t, dialect, "_create_index.sql") {
			for _, n := range ParseIndexNames(txt) {
				names[n] = true
			}
		}
		if !names["idx_t_block_type"] || !names["idx_t_expires_at"] {
			t.Errorf("方言 %s 索引名提取不完整: %v", dialect, names)
		}
	}
}

// TestSplitStatements 字符串/注释内分号不误切；多语句切分-拼接往返一致。
func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "字符串与注释内分号",
			in:   "INSERT INTO t VALUES('a;b'); -- 注释; 里的分号\nUPDATE t SET v=1;/* 块注释; */ DELETE FROM t;",
			want: []string{"INSERT INTO t VALUES('a;b')", "-- 注释; 里的分号\nUPDATE t SET v=1", "/* 块注释; */ DELETE FROM t"},
		},
		{
			name: "纯注释段丢弃",
			in:   "-- 头部说明; 带分号\n\nUPDATE t SET v=1;",
			want: []string{"-- 头部说明; 带分号\n\nUPDATE t SET v=1"},
		},
		{
			name: "单语句无分号结尾",
			in:   "SELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			name: "转义引号内分号",
			in:   "INSERT INTO t VALUES('it''s;a');",
			want: []string{"INSERT INTO t VALUES('it''s;a')"},
		},
	}
	for _, tc := range cases {
		got := SplitStatements(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%s: 切分数量不符 got=%d want=%d: %#v", tc.name, len(got), len(tc.want), got)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: 第 %d 段不符 got=%q want=%q", tc.name, i, got[i], tc.want[i])
			}
		}
		// 往返一致：切分后以分号拼接，再切分结果不变
		again := SplitStatements(strings.Join(got, ";\n"))
		if len(again) != len(got) {
			t.Errorf("%s: 往返切分数量不符 got=%d want=%d", tc.name, len(again), len(got))
		}
	}
}

// TestSplitStatementsGeneratedSQL 生成的多段 DDL（带注释分隔）可逐条执行拆分（B2/B3 依赖）。
func TestSplitStatementsGeneratedSQL(t *testing.T) {
	sqlText := `-- t · 缺表
CREATE TABLE IF NOT EXISTS t (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ip TEXT NOT NULL
);
-- t · 缺列
ALTER TABLE t ADD COLUMN title TEXT NOT NULL DEFAULT '';
`
	got := SplitStatements(sqlText)
	if len(got) != 2 {
		t.Fatalf("应拆出 2 条语句，got %d: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[1], "-- t · 缺列") {
		t.Errorf("语句段应保留前置注释便于结果展示，got %q", got[1])
	}
}
