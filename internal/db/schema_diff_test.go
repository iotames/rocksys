package db

// schema_diff_test.go：diff 分类矩阵（A-F 全级别）+ 归一化用例 + sqlite 内存库 catalog 实测。

import (
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 注册 sqlite 驱动（内存库实测 catalog）
)

// expectedIPBlacklistDDL 构造期望 DDL（取自真实脚本的列集合）。
const expectedIPBlacklistDDL = `CREATE TABLE IF NOT EXISTS ip_blacklist (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ip          TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL DEFAULT '',
    block_type  INTEGER NOT NULL DEFAULT 1,
    hit_count   INTEGER NOT NULL DEFAULT 0,
    expires_at  DATETIME,
    deleted_at  DATETIME,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
)`

// catalogIPBlacklist 构造实际列（与期望一致的完整集合）。
func catalogIPBlacklist() []CatalogColumn {
	defEmpty := "''"
	def1 := "1"
	def0 := "0"
	return []CatalogColumn{
		{Name: "id", TypeFull: "INTEGER", Nullable: "NO", Extra: "auto_increment"},
		{Name: "ip", TypeFull: "TEXT", Nullable: "NO"},
		{Name: "title", TypeFull: "TEXT", Nullable: "NO", Default: &defEmpty},
		{Name: "block_type", TypeFull: "INTEGER", Nullable: "NO", Default: &def1},
		{Name: "hit_count", TypeFull: "INTEGER", Nullable: "NO", Default: &def0},
		{Name: "expires_at", TypeFull: "DATETIME", Nullable: "YES"},
		{Name: "deleted_at", TypeFull: "DATETIME", Nullable: "YES"},
		{Name: "created_at", TypeFull: "DATETIME", Nullable: "NO"},
		{Name: "updated_at", TypeFull: "DATETIME", Nullable: "NO"},
	}
}

// TestDiffTableNoDiff 完全一致 → 零差异。
func TestDiffTableNoDiff(t *testing.T) {
	items := DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: catalogIPBlacklist()})
	if len(items) != 0 {
		t.Errorf("结构一致应零差异，got: %+v", items)
	}
}

// TestDiffTableLevels A-F 全级别矩阵。
func TestDiffTableLevels(t *testing.T) {
	// A 缺表
	items := DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: nil})
	if len(items) != 1 || items[0].Level != "A" || !items[0].Auto {
		t.Errorf("缺表应产出 A 级自动项，got: %+v", items)
	}

	// B 缺普通列（hit_count）
	cols := catalogIPBlacklist()[:4]
	items = DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: cols})
	var foundB *DiffItem
	for i := range items {
		if items[i].Level == "B" && items[i].Object == "hit_count" {
			foundB = &items[i]
		}
	}
	if foundB == nil || !foundB.Auto {
		t.Fatalf("缺普通列应产出 B 级自动项，got: %+v", items)
	}
	if !strings.Contains(foundB.Expected, "INTEGER NOT NULL DEFAULT 0") {
		t.Errorf("B 级 Expected 应为列定义原文 Raw，got %q", foundB.Expected)
	}

	// C 缺 UNIQUE/PK/自增列（ip 含 UNIQUE）
	items = DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL,
		ActualCols: []CatalogColumn{catalogIPBlacklist()[0], catalogIPBlacklist()[3]}}) // 缺 ip/title 等
	var foundC bool
	for _, it := range items {
		if it.Level == "C" && it.Object == "ip" {
			foundC = true
		}
		if it.Level == "B" && it.Object == "ip" {
			t.Errorf("UNIQUE 列缺失误判为 B 级自动项: %+v", it)
		}
	}
	if !foundC {
		t.Errorf("缺 UNIQUE 列应产出 C 级需人工项，got: %+v", items)
	}

	// D 缺索引
	items = DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL,
		ActualCols:    catalogIPBlacklist(),
		ExpectedIndex: "CREATE INDEX IF NOT EXISTS idx_t_block_type ON t(block_type)",
		ActualIndexes: []string{"idx_t_expires_at"}})
	if len(items) != 1 || items[0].Level != "D" || !items[0].Auto {
		t.Errorf("缺索引应产出 D 级自动项，got: %+v", items)
	}

	// E 类型/非空/默认值不一致（仅提示）
	cols = catalogIPBlacklist()
	cols[4].TypeFull = "BIGINT" // hit_count 期望 INTEGER
	items = DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: cols})
	if len(items) != 1 || items[0].Level != "E" || items[0].Auto || items[0].Object != "hit_count" {
		t.Errorf("类型不一致应产出 E 级仅提示项，got: %+v", items)
	}
	cols = catalogIPBlacklist()
	cols[6].Nullable = "NO" // deleted_at 期望可空
	items = DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: cols})
	if len(items) != 1 || items[0].Level != "E" {
		t.Errorf("非空不一致应产出 E 级仅提示项，got: %+v", items)
	}

	// F 多余列（仅提示）
	cols = append(catalogIPBlacklist(), CatalogColumn{Name: "legacy_col", TypeFull: "TEXT", Nullable: "YES"})
	items = DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: cols})
	if len(items) != 1 || items[0].Level != "F" || items[0].Object != "legacy_col" {
		t.Errorf("多余列应产出 F 级仅提示项，got: %+v", items)
	}
}

// TestDiffNormalization 归一化：pg serial ↔ integer+nextval、timestamptz 别名、varchar 别名。
func TestDiffNormalization(t *testing.T) {
	// pg SERIAL 列：期望 bigserial，实际 integer + auto_increment 标记，不应误报 E
	pgDDL := `CREATE TABLE IF NOT EXISTS t (id BIGSERIAL PRIMARY KEY, v TEXT)`
	cols := []CatalogColumn{
		{Name: "id", TypeFull: "bigint", Nullable: "NO", Default: strPtr("nextval('t_id_seq'::regclass)"), Extra: "auto_increment"},
		{Name: "v", TypeFull: "text", Nullable: "YES"},
	}
	if items := DiffTable(DiffInput{Table: "t", ExpectedDDL: pgDDL, ActualCols: cols}); len(items) != 0 {
		t.Errorf("pg SERIAL 归一化后应零差异，got: %+v", items)
	}

	// timestamptz / varchar 别名
	if got := normType("Timestamp With Time Zone"); got != "timestamptz" {
		t.Errorf("timestamptz 别名归一失败: %q", got)
	}
	if got := normType("character varying(64)"); got != "varchar(64)" {
		t.Errorf("varchar 别名归一失败: %q", got)
	}

	// 默认值归一：pg ''::text ↔ 脚本 ''；数字 0 ↔ 0
	if diffDefaults(strPtr("''"), strPtr("''::text")) {
		t.Errorf("pg 类型转换默认值应归一为一致")
	}
	if diffDefaults(strPtr("0"), strPtr("0")) {
		t.Errorf("相同数字默认值不应判差异")
	}
	if !diffDefaults(nil, strPtr("0")) {
		t.Errorf("未声明 vs 有默认值应判差异")
	}
	if diffDefaults(nil, nil) {
		t.Errorf("双方均未声明默认值不应判差异")
	}
}

func strPtr(s string) *string { return &s }

// TestGenerateSQL 自动项生成：A 建表原文 / B ADD COLUMN / D 索引原文；仅提示项不生成。
func TestGenerateSQL(t *testing.T) {
	src, err := EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource: %v", err)
	}
	specs := []TableSpec{{Table: "t", CreateScript: "ip_blacklist_create_table.sql", IndexScript: "ip_blacklist_create_index.sql"}}
	items := []DiffItem{
		{Level: "A", Auto: true, Table: "t", Object: "t", Note: "缺表：可用生成的建表脚本原文创建"},
		{Level: "B", Auto: true, Table: "t", Object: "warn_times", Expected: "INTEGER NOT NULL DEFAULT 0", Note: "缺普通列：可自动 ADD COLUMN"},
		{Level: "D", Auto: true, Table: "t", Object: "idx_t_x", Note: "缺索引：可自动创建"},
		{Level: "E", Auto: false, Table: "t", Object: "hit_count", Note: "类型不一致：仅提示"},
	}
	sqlText, err := GenerateSQL(items, specs, src)
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if !strings.Contains(sqlText, "ALTER TABLE t ADD COLUMN INTEGER NOT NULL DEFAULT 0") {
		t.Errorf("B 级应生成 ADD COLUMN 语句，got:\n%s", sqlText)
	}
	if !strings.Contains(sqlText, "CREATE TABLE IF NOT EXISTS t (") {
		t.Errorf("A 级应生成 {table} 替换后的建表原文，got:\n%s", sqlText)
	}
	if !strings.Contains(sqlText, "CREATE INDEX IF NOT EXISTS idx_t_block_type") {
		t.Errorf("D 级应生成索引原文，got:\n%s", sqlText)
	}
	if !strings.Contains(sqlText, "-- t · 缺表") {
		t.Errorf("各段应带「表名 · 差异说明」注释分隔，got:\n%s", sqlText)
	}
	// 生成文本可被拆句器逐条拆分（执行器依赖）
	if n := len(SplitStatements(sqlText)); n < 4 {
		t.Errorf("生成 SQL 应可拆出 ≥4 条语句（建表2+索引2+ALTER），got %d", n)
	}
}

// TestCatalogSQLite SQLite 内存库实测 catalog 读取与真实脚本比对（零差异闭环）。
func TestCatalogSQLite(t *testing.T) {
	d, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	defer d.Close()

	// 用真实建表脚本建表（逐条执行，与组件 EnsureTable 同语义）
	ddl, err := d.SQL("ip_blacklist_create_table.sql")
	if err != nil {
		t.Fatalf("读取建表脚本: %v", err)
	}
	ddl = strings.ReplaceAll(ddl, "{table}", "ip_blacklist")
	for _, stmt := range SplitStatements(ddl) {
		if _, err := d.edb.GetSqlDB().Exec(stmt); err != nil {
			t.Fatalf("执行建表语句失败: %v\n%s", err, stmt)
		}
	}

	cols, err := d.CatalogColumns("ip_blacklist")
	if err != nil {
		t.Fatalf("CatalogColumns: %v", err)
	}
	if len(cols) != 10 {
		t.Fatalf("ip_blacklist 应有 10 列（含 warn_times），got %d: %+v", len(cols), cols)
	}
	if cols[1].Name != "ip" || cols[1].Nullable != "NO" {
		t.Errorf("列归一化结果异常: %+v", cols[1])
	}

	// 真实脚本 vs 真实库：零差异
	idx, err := d.SQL("ip_blacklist_create_index.sql")
	if err != nil {
		t.Fatalf("读取索引脚本: %v", err)
	}
	idx = strings.ReplaceAll(idx, "{table}", "ip_blacklist")
	for _, stmt := range SplitScriptStatements(idx) {
		if _, err := d.edb.GetSqlDB().Exec(stmt); err != nil {
			t.Fatalf("执行索引语句失败: %v\n%s", err, stmt)
		}
	}
	indexes, err := d.CatalogIndexes("ip_blacklist")
	if err != nil {
		t.Fatalf("CatalogIndexes: %v", err)
	}
	items := DiffTable(DiffInput{Table: "ip_blacklist", ExpectedDDL: ddl, ExpectedIndex: idx,
		ActualCols: cols, ActualIndexes: indexes})
	if len(items) != 0 {
		t.Errorf("真实脚本建的表应零差异，got: %+v", items)
	}

	// sqlite 内部表过滤：AUTOINCREMENT 产生 sqlite_sequence，不得出现在 CatalogTables
	if _, err := d.edb.GetSqlDB().Exec("INSERT INTO ip_blacklist (ip, title, created_at, updated_at) VALUES ('1.2.3.4', 't', '2026-01-01', '2026-01-01')"); err != nil {
		t.Fatalf("插入触发 sqlite_sequence: %v", err)
	}
	tables, err := d.CatalogTables()
	if err != nil {
		t.Fatalf("CatalogTables: %v", err)
	}
	for _, tb := range tables {
		if strings.HasPrefix(tb, "sqlite_") {
			t.Errorf("内部表 %s 未被过滤", tb)
		}
	}
	if len(tables) == 0 || tables[0] != "ip_blacklist" {
		t.Errorf("CatalogTables 应含 ip_blacklist，got: %v", tables)
	}

	// 缺列表场景：手工建旧版表（少 hit_count）→ B 级
	if _, err := d.edb.GetSqlDB().Exec("CREATE TABLE old_version (id INTEGER PRIMARY KEY AUTOINCREMENT, ip TEXT NOT NULL)"); err != nil {
		t.Fatalf("建旧版表: %v", err)
	}
	oldCols, err := d.CatalogColumns("old_version")
	if err != nil {
		t.Fatalf("CatalogColumns(old_version): %v", err)
	}
	items = DiffTable(DiffInput{Table: "old_version", ExpectedDDL: expectedIPBlacklistDDL, ActualCols: oldCols})
	var hasB bool
	for _, it := range items {
		if it.Level == "B" && it.Object == "hit_count" {
			hasB = true
		}
	}
	if !hasB {
		t.Errorf("旧版表缺列应产出 B 级项，got: %+v", items)
	}

	// 表不存在：catalog 返回空集（上层判定缺表），不报错
	missing, err := d.CatalogColumns("no_such_table")
	if err != nil {
		t.Errorf("查询不存在表不应报错: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("不存在表应返回空结果集，got %+v", missing)
	}
}
