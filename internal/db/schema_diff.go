package db

// schema_diff.go：期望结构（建表/建索引脚本解析）vs 实际结构（catalog 查询）的差异
// 分类引擎与安全项 SQL 生成。分级口径（docs/DB_SCHEMA_SYNC_PLAN.md 决策 1，保守）：
//
//	A 缺表            → 自动（建表脚本原文，多语句由拆句器逐条执行）
//	B 缺普通列        → 自动（ALTER TABLE ADD COLUMN，取脚本列定义原文 Raw，方言天然正确）
//	C 缺 PK/UNIQUE/自增列 → 需人工（SQLite 不支持 ADD 相关约束，跨方言不可靠，不生成）
//	D 缺索引          → 自动（建索引脚本原文，幂等 IF NOT EXISTS）
//	E 类型/非空/默认值不一致 → 仅提示（SQLite 改列需重建表，跨方言不可靠，不生成）
//	F 库中多余列/表   → 仅提示（不生成 DROP，可能含数据或为历史遗留）

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DiffItem 单条差异（即端点响应 items 的元素）。
type DiffItem struct {
	Level    string `json:"level"`    // A-F 分级
	Auto     bool   `json:"auto"`     // 是否自动生成 SQL（A/B/D）
	Table    string `json:"table"`    // 所属表名
	Object   string `json:"object"`   // 差异对象（表名/列名/索引名）
	Expected string `json:"expected"` // 期望（脚本侧）
	Actual   string `json:"actual"`   // 实际（catalog 侧）
	Note     string `json:"note"`     // 处理建议
}

// DiffInput 单表比对的输入（期望脚本已做 {table} 占位符替换）。
type DiffInput struct {
	Table         string
	ExpectedDDL   string // 建表脚本文本（{table} 已替换）
	ExpectedIndex string // 建索引脚本文本（可空 = 无独立索引脚本）
	ActualCols    []CatalogColumn
	ActualIndexes []string
}

// typeAliases 归一化别名（跨表达差异，仅影响 E 级判定准确性）。
// 按字母 token 整词替换（不能子串替换——bigint 含 int 会被误伤）。
var typeAliases = map[string]string{
	"timestampwithtimezone":    "timestamptz",
	"timestampwithouttimezone": "timestamp",
	"charactervarying":         "varchar",
	"int":                      "integer", // pg 脚本 INT ↔ format_type integer
}

// typeTokenRe 归一化串中的字母 token（非字母字符原样保留，如括号与数字）。
var typeTokenRe = regexp.MustCompile(`[a-z]+`)

// normType 类型串归一化：小写去空白 + 字母 token 整词别名。
func normType(t string) string {
	n := strings.ToLower(strings.Join(strings.Fields(t), ""))
	return typeTokenRe.ReplaceAllStringFunc(n, func(tok string) string {
		if to, ok := typeAliases[tok]; ok {
			return to
		}
		return tok
	})
}

// serialBase 自增列的期望类型归一：pg 脚本写 SERIAL/BIGSERIAL，catalog 呈
// integer/bigint（+nextval 默认值），比对前对齐到基础类型。
var serialBase = map[string]string{
	"bigserial": "bigint", "serial": "integer", "smallserial": "smallint",
}

// normDefault 默认值字面归一：剥 pg 类型转换（”::text）、去引号。
func normDefault(v string) string {
	if i := strings.Index(v, "::"); i >= 0 {
		v = v[:i]
	}
	return strings.Trim(v, "'")
}

// diffDefaults 默认值是否一致（nil = 未声明；catalog NULL 亦视为未声明）。
func diffDefaults(expected *string, actual *string) bool {
	if expected == nil && actual == nil {
		return false
	}
	if expected == nil || actual == nil {
		return true
	}
	return normDefault(*expected) != normDefault(*actual)
}

// DiffTable 单表比对，产出 A-F 分级差异（无差异返回空切片）。
func DiffTable(in DiffInput) []DiffItem {
	var items []DiffItem
	expected, err := ParseCreateTable(in.ExpectedDDL)
	if err != nil {
		return []DiffItem{{Level: "C", Auto: false, Table: in.Table, Object: in.Table,
			Expected: "建表脚本可解析", Actual: "解析失败: " + err.Error(),
			Note: "期望结构解析失败（脚本异常），请人工检查建表脚本"}}
	}

	// A 缺表：实际列集为空（catalog 对不存在表返回空结果集）
	if len(in.ActualCols) == 0 {
		return []DiffItem{{Level: "A", Auto: true, Table: in.Table, Object: in.Table,
			Expected: "表存在（以建表脚本为准）", Actual: "表不存在",
			Note: "缺表：可用生成的建表脚本原文创建（多语句自动拆条执行）"}}
	}

	actual := map[string]CatalogColumn{}
	for _, c := range in.ActualCols {
		actual[c.Name] = c
	}
	// 表级 PRIMARY KEY/UNIQUE 约束涉及的列（mysql 表级 UNIQUE KEY 等不产出 ColumnDef，
	// 缺列若按 B 级自动补列会静默丢失约束，须降 C 级需人工）。
	keyCols := ParseTableKeyColumns(in.ExpectedDDL)
	for _, e := range expected {
		a, ok := actual[e.Name]
		if !ok {
			// C：缺 PK/UNIQUE/自增列（SQLite 不可 ADD，不生成）；否则 B 自动补列
			if e.IsPK || e.IsUnique || e.IsAutoInc || keyCols[strings.ToLower(e.Name)] {
				items = append(items, DiffItem{Level: "C", Auto: false, Table: in.Table, Object: e.Name,
					Expected: e.Raw, Actual: "列不存在",
					Note: "缺主键/唯一/自增列（含表级约束列）：SQLite 不支持 ADD 相关约束、跨方言不可靠，需人工处理（建议重建表迁移数据）"})
			} else {
				items = append(items, DiffItem{Level: "B", Auto: true, Table: in.Table, Object: e.Name,
					Expected: e.Raw, Actual: "列不存在",
					Note: "缺普通列：可自动 ADD COLUMN（取脚本列定义原文，方言天然正确）"})
			}
			continue
		}
		// E：类型 / 非空 / 默认值不一致（仅提示）。
		// 归一化口径：主键隐含 NOT NULL（sqlite/pg 脚本中 PK 列可不写 NOT NULL 而 catalog 报 NO）；
		// 自增列不比对默认值（nextval/auto_increment 等序列默认值是实现细节）。
		expType := normType(e.Type)
		if e.IsAutoInc {
			if base, ok := serialBase[expType]; ok {
				expType = base
			}
		}
		expNotNull := e.NotNull || e.IsPK
		// 实际侧非空归一：主键/自增列在 catalog 可能报 nullable=YES（sqlite 的
		// INTEGER PRIMARY KEY notnull=0），主键/自增即事实非空。
		actualNotNull := a.Nullable == "NO" || a.Extra == "auto_increment" || a.Extra == "primary_key" ||
			strings.Contains(a.Extra, "primary_key")
		if expType != normType(a.TypeFull) || expNotNull != actualNotNull ||
			(!e.IsAutoInc && diffDefaults(e.Default, a.Default)) {
			items = append(items, DiffItem{Level: "E", Auto: false, Table: in.Table, Object: e.Name,
				Expected: colSummary(e), Actual: colSummaryActual(a),
				Note: "类型/非空/默认值不一致：仅提示不生成（SQLite 改列需重建表、跨方言不可靠），请人工评估"})
		}
	}
	// F：库中多余列（不生成 DROP）
	for _, a := range in.ActualCols {
		found := false
		for _, e := range expected {
			if e.Name == a.Name {
				found = true
				break
			}
		}
		if !found {
			items = append(items, DiffItem{Level: "F", Auto: false, Table: in.Table, Object: a.Name,
				Expected: "无此列（以建表脚本为准）", Actual: a.TypeFull,
				Note: "库中多余列：不自动 DROP（可能含数据或历史遗留），请人工确认后自行处理"})
		}
	}
	// D：缺索引（建索引脚本原文，幂等）
	if in.ExpectedIndex != "" {
		actualIdx := map[string]bool{}
		for _, n := range in.ActualIndexes {
			actualIdx[n] = true
		}
		for _, name := range ParseIndexNames(in.ExpectedIndex) {
			if !actualIdx[name] {
				items = append(items, DiffItem{Level: "D", Auto: true, Table: in.Table, Object: name,
					Expected: name, Actual: "索引不存在",
					Note: "缺索引：可自动创建（仅生成缺失索引的单条语句，不影响已有索引）"})
			}
		}
	}
	return items
}

// colSummary 期望列摘要（E 级展示）。
func colSummary(c ColumnDef) string {
	s := c.Type
	if c.NotNull {
		s += " NOT NULL"
	}
	if c.Default != nil {
		s += " DEFAULT " + *c.Default
	}
	return s
}

// colSummaryActual 实际列摘要（E 级展示）。
func colSummaryActual(c CatalogColumn) string {
	s := c.TypeFull
	if c.Nullable == "NO" {
		s += " NOT NULL"
	}
	if c.Default != nil {
		s += " DEFAULT " + *c.Default
	}
	if c.Extra != "" {
		s += " (" + c.Extra + ")"
	}
	return s
}

// DiffSchema 全清单比对：遍历表清单逐表 DiffTable，附加 F 级「多余表」检测。
func DiffSchema(d *DB, specs []TableSpec) ([]DiffItem, error) {
	var items []DiffItem
	for _, spec := range specs {
		ddl, err := d.SQL(spec.CreateScript)
		if err != nil {
			return nil, fmt.Errorf("db: 读取建表脚本 %s 失败: %w", spec.CreateScript, err)
		}
		ddl = strings.ReplaceAll(ddl, "{table}", spec.Table)
		idx := ""
		if spec.IndexScript != "" {
			if idx, err = d.SQL(spec.IndexScript); err != nil {
				return nil, fmt.Errorf("db: 读取索引脚本 %s 失败: %w", spec.IndexScript, err)
			}
			idx = strings.ReplaceAll(idx, "{table}", spec.Table)
		}
		cols, err := d.CatalogColumns(spec.Table)
		if err != nil {
			return nil, fmt.Errorf("db: 查询表 %s 实际列结构失败: %w", spec.Table, err)
		}
		var indexes []string
		if spec.IndexScript != "" {
			if indexes, err = d.CatalogIndexes(spec.Table); err != nil {
				return nil, fmt.Errorf("db: 查询表 %s 实际索引失败: %w", spec.Table, err)
			}
		}
		items = append(items, DiffTable(DiffInput{
			Table: spec.Table, ExpectedDDL: ddl, ExpectedIndex: idx,
			ActualCols: cols, ActualIndexes: indexes,
		})...)
	}
	// F 级：库中存在但未注册的多余表
	tables, err := d.CatalogTables()
	if err != nil {
		return nil, fmt.Errorf("db: 查询库内表清单失败: %w", err)
	}
	registered := map[string]bool{}
	for _, s := range specs {
		registered[s.Table] = true
	}
	for _, t := range tables {
		if !registered[t] {
			items = append(items, DiffItem{Level: "F", Auto: false, Table: t, Object: t,
				Expected: "未注册（不在表清单）", Actual: "库中存在",
				Note: "库中多余表：不自动 DROP（可能含数据或历史遗留），请人工确认"})
		}
	}
	sortDiffItems(items)
	return items, nil
}

// sortDiffItems 稳定排序：按级别（A-F）再按表名，输出顺序可预期。
func sortDiffItems(items []DiffItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Level != items[j].Level {
			return items[i].Level < items[j].Level
		}
		return items[i].Table < items[j].Table
	})
}

// stmtStartRe 语句起始关键字（用于无分号脚本的「每行一条」分条判定）。
var stmtStartRe = regexp.MustCompile(`(?i)^\s*(CREATE|ALTER|DROP|INSERT|UPDATE|DELETE|SELECT|PRAGMA|COMMENT|REPLACE|TRUNCATE)\b`)

// nextIsStatementStart 判断 from 之后的下一段非空非注释内容是否为语句起始。
func nextIsStatementStart(txt string, from int) bool {
	rest := txt[from:]
	for {
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if trimmed == "" {
			return false
		}
		if strings.HasPrefix(trimmed, "--") {
			idx := strings.IndexByte(trimmed, '\n')
			if idx < 0 {
				return false
			}
			rest = trimmed[idx+1:]
			continue
		}
		return stmtStartRe.MatchString(trimmed)
	}
}

// SplitScriptStatements 把建表/建索引脚本文本切成单条可执行语句。兼容两种分条约定：
// ①语句以分号结尾（pg 建表 + COMMENT ON）；②每行一条且无分号（sqlite/mysql 建表与索引脚本，
// 依「下一行以语句起始关键字开头」判定边界）。多行 CREATE TABLE 的列定义块在括号内，不误切；
// 每条语句保留其前置注释（执行无碍、结果展示可读）。
func SplitScriptStatements(txt string) []string {
	st := &tokenState{}
	depth := 0
	var out []string
	start := 0
	flush := func(end int) {
		seg := strings.TrimSpace(txt[start:end])
		if strings.TrimSpace(stripComments(seg)) != "" {
			out = append(out, seg)
		}
		start = end + 1
	}
	for i := 0; i < len(txt); i++ {
		if !st.step(txt, i) {
			continue
		}
		switch txt[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				flush(i)
			}
		case '\n':
			if depth == 0 && nextIsStatementStart(txt, i+1) {
				flush(i)
			}
		}
	}
	if start < len(txt) {
		seg := strings.TrimSpace(txt[start:])
		if strings.TrimSpace(stripComments(seg)) != "" {
			out = append(out, seg)
		}
	}
	return out
}

// joinStatements 把多语句脚本文本规范化为「每条以分号结尾、换行分隔」的形式，
// 供拆句器（SplitStatements）逐条执行。
func joinStatements(txt string) string {
	stmts := SplitScriptStatements(strings.TrimSpace(txt))
	for i, s := range stmts {
		s = strings.TrimSpace(s)
		if !strings.HasSuffix(s, ";") {
			s += ";"
		}
		stmts[i] = s
	}
	return strings.Join(stmts, "\n")
}

// GenerateSQL 把自动项（A/B/D）拼成可执行 SQL 文本（各段带 `-- 表名 · 差异说明` 注释分隔）。
// A = 建表脚本原文（{table} 已替换）；B = ALTER TABLE ADD COLUMN <Raw>；
// D = 仅缺失索引的单条 CREATE INDEX（整份脚本重放会在已有索引处报 Duplicate key，
// 配合执行器「遇错即停」会导致剩余索引永远补不齐，故必须逐索引生成）。
func GenerateSQL(items []DiffItem, specs []TableSpec, source SQLSource) (string, error) {
	specByTable := map[string]TableSpec{}
	for _, s := range specs {
		specByTable[s.Table] = s
	}
	var b strings.Builder
	for _, it := range items {
		if !it.Auto {
			continue
		}
		spec, ok := specByTable[it.Table]
		if !ok {
			return "", fmt.Errorf("db: 差异表 %s 不在表清单中，无法生成 SQL", it.Table)
		}
		var stmt string
		switch it.Level {
		case "A":
			txt, err := source.SQL(spec.CreateScript)
			if err != nil {
				return "", fmt.Errorf("db: 读取建表脚本 %s 失败: %w", spec.CreateScript, err)
			}
			stmt = joinStatements(strings.ReplaceAll(txt, "{table}", it.Table))
			// 建表附带配套索引（与组件 EnsureTable「建表+索引」同语义）：
			// 只生成建表原文会导致新库一轮同步后仍缺全部索引，需二次检查才能补齐。
			if spec.IndexScript != "" {
				idx, err := source.SQL(spec.IndexScript)
				if err != nil {
					return "", fmt.Errorf("db: 读取索引脚本 %s 失败: %w", spec.IndexScript, err)
				}
				stmt += "\n" + joinStatements(strings.ReplaceAll(idx, "{table}", it.Table))
			}
		case "B":
			stmt = fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", it.Table, it.Expected)
		case "D":
			if spec.IndexScript == "" {
				return "", fmt.Errorf("db: 表 %s 无索引脚本，无法生成缺索引 SQL", it.Table)
			}
			txt, err := source.SQL(spec.IndexScript)
			if err != nil {
				return "", fmt.Errorf("db: 读取索引脚本 %s 失败: %w", spec.IndexScript, err)
			}
			stmts, err := indexStatementMap(txt, it.Table)
			if err != nil {
				return "", err
			}
			s, ok := stmts[it.Object]
			if !ok {
				return "", fmt.Errorf("db: 索引脚本中未找到索引 %s 的建索引语句，请人工检查脚本", it.Object)
			}
			stmt = s
		default:
			return "", fmt.Errorf("db: 差异级别 %s 不支持自动生成 SQL", it.Level)
		}
		fmt.Fprintf(&b, "-- %s · %s\n%s\n\n", it.Table, it.Note, stmt)
	}
	return b.String(), nil
}

// indexStatementMap 把建索引脚本按语句拆开，归一为「索引名 → 单条带分号语句」映射
// （{table} 已替换；一条语句声明多个索引时映射到每个名字，生成端按已发出去重）。
func indexStatementMap(indexScript, table string) (map[string]string, error) {
	joined := joinStatements(strings.ReplaceAll(indexScript, "{table}", table))
	out := map[string]string{}
	emitted := map[string]bool{}
	for _, stmt := range SplitStatements(joined) {
		if emitted[stmt] {
			continue
		}
		emitted[stmt] = true
		s := strings.TrimRight(strings.TrimSpace(stmt), ";") + ";"
		for _, name := range ParseIndexNames(stmt) {
			out[name] = s
		}
	}
	return out, nil
}
