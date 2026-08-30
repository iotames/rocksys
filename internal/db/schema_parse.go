package db

// schema_parse.go：DDL 解析器与多语句拆分（与数据库连接无关的纯函数能力）。
//
// 用途（数据库表结构同步功能，见 docs/DB_SCHEMA_SYNC_PLAN.md）：
//  1. ParseCreateTable —— 解析建表脚本得到期望列结构（期望结构权威来源 = 运行期 SQLSource 脚本，
//     与各挂件实际建表同源）；
//  2. ParseIndexNames —— 提取建索引脚本的索引名集合；
//  3. SplitStatements —— 字符级多语句拆分（感知字符串字面量与注释内的分号/括号，
//     供 /admin/db/exec 逐条执行；按行拆分的 SplitSQLStatements 保留给约定"每行一条"的建表脚本）。
//
// 注意：脚本方言各异（sqlite/pg 列后跟 -- 注释、mysql 列内嵌 COMMENT '...' 且注释文本
// 可能含 ASCII 括号/分号），因此所有扫描均感知单引号字符串（'' 转义）、双引号与反引号。

import (
	"fmt"
	"regexp"
	"strings"
)

// TableSpec 表清单条目：实际表名 + 建表/建索引脚本文件名。
// 注册点在 cmd/rocksys/main.go 装配处（各表名在那里已知：字面量/常量/配置实值），
// 表名无法从脚本文件名推断（如 mq_create_table.sql 的实际表名是 outbox）。
type TableSpec struct {
	Table        string // 实际表名（运行时真实表名，非文件名）
	CreateScript string // 建表脚本文件名（sql/<dbtype>/ 下，含 {table} 占位符）
	IndexScript  string // 建索引脚本文件名（可空 = 该表无独立索引脚本）
}

// ColumnDef 期望结构的单列定义（由建表 DDL 解析得出）。
type ColumnDef struct {
	Name      string  // 列名（剥离引号包裹）
	Type      string  // 类型串（含括号部分，如 VARCHAR(45)）
	NotNull   bool    // 是否 NOT NULL
	Default   *string // DEFAULT 字面值（nil = 未声明默认值）
	Raw       string  // 该列在脚本中的原始定义文本（注释已剥离），供 ADD COLUMN 原样复用（方言天然正确）
	IsPK      bool    // 列内 PRIMARY KEY（表级约束不进入 ColumnDef）
	IsUnique  bool    // 列内 UNIQUE（精确关键字匹配；表级 UNIQUE KEY 不进入 ColumnDef）
	IsAutoInc bool    // 自增：AUTOINCREMENT(sqlite) / AUTO_INCREMENT(mysql) / SERIAL|BIGSERIAL(pg) / GENERATED ... AS IDENTITY(pg)
}

// 约束关键字：列定义块中以上列关键字开头的行是表级约束，不作为列解析
// （mysql 的 UNIQUE KEY uk_x (ip)、pg/sqlite 的表级 PRIMARY KEY(a,b) 等）。
var tableConstraintKeywords = map[string]bool{
	"PRIMARY": true, "UNIQUE": true, "KEY": true, "INDEX": true,
	"CONSTRAINT": true, "CHECK": true, "EXCLUDE": true, "FOREIGN": true,
}

// constraintKeyRe 匹配约束段中的键约束类型关键字（词边界，避免约束名含关键字字样误判）。
var constraintKeyRe = regexp.MustCompile(`(?i)\b(PRIMARY\s+KEY|UNIQUE)\b`)

// DEFAULT 值的终止关键字（其后跟的 token 不再属于默认值表达式）。
var defaultStopKeywords = map[string]bool{
	"NOT": true, "NULL": true, "PRIMARY": true, "UNIQUE": true, "COMMENT": true,
	"AUTO_INCREMENT": true, "AUTOINCREMENT": true, "GENERATED": true, "CHECK": true,
	"REFERENCES": true, "ON": true, "CONSTRAINT": true, "COLLATE": true,
}

// stripComments 剥离 SQL 文本中的 -- 行注释与 /* */ 块注释（跳过字符串字面量内部）。
func stripComments(s string) string {
	var b strings.Builder
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			quote := c
			b.WriteByte(c)
			i++
			for i < n {
				if s[i] == quote {
					// '' / "" / `` 转义：连续两个同引号视为字面引号
					if i+1 < n && s[i+1] == quote {
						b.WriteByte(quote)
						b.WriteByte(quote)
						i += 2
						continue
					}
					b.WriteByte(quote)
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
		case c == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// tokenState 字符扫描状态（字符串/注释感知）。
type tokenState struct {
	inSingle, inDouble, inBacktick, inLine, inBlock bool
	skipQuote                                       bool // 字符串内转义引号（'' 成对）的第二个字符待跳过
}

// step 前进一个字符并更新状态，返回该字符是否处于"普通"位置（非字符串/注释内部）。
func (st *tokenState) step(s string, i int) bool {
	c := s[i]
	switch {
	case st.inLine:
		if c == '\n' {
			st.inLine = false
		}
		return false
	case st.inBlock:
		if c == '*' && i+1 < len(s) && s[i+1] == '/' {
			st.inBlock = false
			return false // 消费 '/' 由外层 i+1 自然跳过，此处仅需关闭状态
		}
		return false
	case st.inSingle || st.inDouble || st.inBacktick:
		quote := byte('\'')
		if st.inDouble {
			quote = '"'
		} else if st.inBacktick {
			quote = '`'
		}
		if st.skipQuote { // 上一个引号是转义字面量（'' 前一个），本引号与其成对，仍在字符串内
			st.skipQuote = false
			return false
		}
		if c == quote {
			if i+1 < len(s) && s[i+1] == quote { // '' / "" / `` 转义：本引号为字面量，下一引号需跳过
				st.skipQuote = true
				return false
			}
			st.inSingle, st.inDouble, st.inBacktick = false, false, false
		}
		return false
	case c == '\'':
		st.inSingle = true
	case c == '"':
		st.inDouble = true
	case c == '`':
		st.inBacktick = true
	case c == '-' && i+1 < len(s) && s[i+1] == '-':
		st.inLine = true
		return false
	case c == '/' && i+1 < len(s) && s[i+1] == '*':
		st.inBlock = true
		return false
	}
	return true
}

// ParseCreateTable 解析建表 DDL，返回列定义集合。
// 输入须为已完成 {table} 占位符替换的最终 DDL（沿用各组件现有替换约定）。
// 表级约束（PRIMARY KEY(a,b) / UNIQUE KEY / CHECK 等）不产出 ColumnDef；
// mysql 表尾选项（) DEFAULT CHARSET=... COMMENT='...'）不参与解析。
func ParseCreateTable(ddl string) ([]ColumnDef, error) {
	block, err := createTableBlock(ddl)
	if err != nil {
		return nil, err
	}

	var cols []ColumnDef
	for _, part := range splitTopLevel(block, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		first, rest := cutToken(part)
		key := strings.ToUpper(first)
		if tableConstraintKeywords[key] {
			continue // 表级约束行
		}
		cols = append(cols, parseColumnDef(strings.Trim(first, "`\""), rest, part))
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("db: 建表 DDL 未解析出任何列定义")
	}
	return cols, nil
}

// createTableBlock 提取 CREATE TABLE 列定义括号内的文本（括号深度 + 字符串感知，
// COMMENT '...(...)...' 内的括号不计数）。
func createTableBlock(ddl string) (string, error) {
	clean := stripComments(ddl)
	upper := strings.ToUpper(clean)
	start := strings.Index(upper, "CREATE TABLE")
	if start < 0 {
		return "", fmt.Errorf("db: DDL 中未找到 CREATE TABLE 语句")
	}
	popen := strings.IndexByte(clean[start:], '(')
	if popen < 0 {
		return "", fmt.Errorf("db: CREATE TABLE 语句缺少列定义括号")
	}
	popen += start

	st := &tokenState{}
	depth := 0
	pclose := -1
	for i := popen; i < len(clean); i++ {
		if !st.step(clean, i) {
			continue
		}
		switch clean[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				pclose = i
			}
		}
		if pclose >= 0 {
			break
		}
	}
	if pclose < 0 {
		return "", fmt.Errorf("db: CREATE TABLE 列定义括号不闭合")
	}
	return clean[popen+1 : pclose], nil
}

// ParseTableKeyColumns 提取表级 PRIMARY KEY / UNIQUE 约束涉及的列名集合（小写、剥离引号）。
// 背景：mysql 表级 `UNIQUE KEY uk_x (ip)`、pg/sqlite 表级 `PRIMARY KEY(a,b)` 不产出
// ColumnDef，若缺列被判为 B 级自动补列，唯一/主键约束会静默丢失——调用方（schema_diff）
// 据此把命中列降为 C 级需人工。返回集合为空 = 无表级键约束。
func ParseTableKeyColumns(ddl string) map[string]bool {
	block, err := createTableBlock(ddl)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, part := range splitTopLevel(block, ',') {
		part = strings.TrimSpace(part)
		first, rest := cutToken(part)
		key := strings.ToUpper(first)
		if key != "PRIMARY" && key != "UNIQUE" && key != "CONSTRAINT" {
			continue // 仅表级主键/唯一约束（KEY/INDEX 为纯索引，CHECK/EXCLUDE/FOREIGN 与列缺失无关）
		}
		if key == "CONSTRAINT" {
			// CONSTRAINT <name> PRIMARY KEY(...)/UNIQUE(...)：看约束类型关键字。
			// 词边界匹配：CHECK 约束名含 UNIQUE/PRIMARY 字样（如 chk_unique_entry）不误判为键约束。
			if !constraintKeyRe.MatchString(rest) {
				continue
			}
		}
		// 取该约束段的第一个顶层括号组（列清单）。
		popen := strings.IndexByte(part, '(')
		if popen < 0 {
			continue
		}
		st := &tokenState{}
		depth := 0
		end := -1
		for i := popen; i < len(part); i++ {
			if !st.step(part, i) {
				continue
			}
			switch part[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		for _, c := range strings.Split(part[popen+1:end], ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			// 剥离引号与长度/排序修饰（如 col(191) DESC）后取列名。
			c = strings.Trim(c, "`\" ")
			if i := strings.IndexByte(c, '('); i >= 0 {
				c = c[:i]
			}
			c = strings.ToLower(strings.TrimSpace(strings.Trim(c, "`\" ")))
			if c != "" {
				out[c] = true
			}
		}
	}
	return out
}

// parseColumnDef 解析单列定义（name 已剥离；rest 为列名后的剩余文本；raw 为原始文本）。
func parseColumnDef(name, rest, raw string) ColumnDef {
	toks := tokenize(rest)
	col := ColumnDef{Name: name, Raw: strings.TrimSpace(raw)}
	if len(toks) > 0 {
		col.Type = toks[0]
	}
	if strings.Contains(strings.ToUpper(col.Type), "SERIAL") {
		col.IsAutoInc = true // pg BIGSERIAL/SERIAL/SMALLSERIAL
	}
	for i := 1; i < len(toks); i++ {
		switch strings.ToUpper(toks[i]) {
		case "NOT":
			if i+1 < len(toks) && strings.EqualFold(toks[i+1], "NULL") {
				col.NotNull = true
				i++
			}
		case "PRIMARY":
			if i+1 < len(toks) && strings.EqualFold(toks[i+1], "KEY") {
				col.IsPK = true
				i++
			}
		case "UNIQUE":
			col.IsUnique = true
		case "AUTOINCREMENT", "AUTO_INCREMENT":
			col.IsAutoInc = true
		case "GENERATED":
			// pg GENERATED ALWAYS AS IDENTITY / BY DEFAULT AS IDENTITY
			for j := i + 1; j < len(toks); j++ {
				if strings.EqualFold(toks[j], "IDENTITY") {
					col.IsAutoInc = true
					break
				}
			}
		case "DEFAULT":
			// 取默认值表达式：直到终止关键字；单引号字符串为单 token
			var vals []string
			j := i + 1
			for ; j < len(toks); j++ {
				if defaultStopKeywords[strings.ToUpper(toks[j])] {
					break
				}
				vals = append(vals, toks[j])
			}
			if len(vals) > 0 {
				v := strings.Join(vals, " ")
				col.Default = &v
				i = j - 1
			}
		case "COMMENT":
			if i+1 < len(toks) && strings.HasPrefix(toks[i+1], "'") {
				i++ // mysql 行内 COMMENT '...'：跳过注释串
			}
		}
	}
	return col
}

// splitTopLevel 按 sep 切分，忽略字符串字面量与括号内部的 sep。
func splitTopLevel(s string, sep byte) []string {
	var out []string
	st := &tokenState{}
	depth := 0
	last := 0
	for i := 0; i < len(s); i++ {
		if !st.step(s, i) {
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

// cutToken 切出首个空白分隔 token，返回 (token, 其余文本)。
func cutToken(s string) (string, string) {
	s = strings.TrimLeft(s, " \t\n\r")
	i := strings.IndexAny(s, " \t\n\r")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

// tokenize 按空白切 token（字符串字面量为完整单 token）。
// 先把 Tab/换行/回车归一为空格：避免 NOT\tNULL 粘连、DECIMAL(10,\n2) 拆断等误切。
func tokenize(s string) []string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
	var out []string
	for _, p := range splitTopLevel(s, ' ') {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// indexNameRe 匹配 CREATE [UNIQUE] INDEX [IF NOT EXISTS] <name>
// mysql 不支持 IF NOT EXISTS（组件侧幂等容错），故该子句可选。
var indexNameRe = regexp.MustCompile(`(?im)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[` + "`" + `"\']?([A-Za-z0-9_{}]+)`)

// ParseIndexNames 提取建索引脚本中的索引名集合（剥离注释后正则匹配）。
func ParseIndexNames(sqlText string) []string {
	matches := indexNameRe.FindAllStringSubmatch(stripComments(sqlText), -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// SplitStatements 字符级多语句拆分：按分号切分，跳过单引号/双引号/反引号字符串
// 与 -- / /* */ 注释内部的分号；丢弃纯注释/空白段（保留语句段自带的前置注释）。
// 供 /admin/db/exec 逐条执行（database/sql 各驱动均不支持一次 Exec 多语句）。
func SplitStatements(sqlText string) []string {
	var out []string
	st := &tokenState{}
	last := 0
	for i := 0; i < len(sqlText); i++ {
		if !st.step(sqlText, i) {
			continue
		}
		if sqlText[i] == ';' {
			if seg := strings.TrimSpace(sqlText[last:i]); hasStatement(seg) {
				out = append(out, seg)
			}
			last = i + 1
		}
	}
	if seg := strings.TrimSpace(sqlText[last:]); hasStatement(seg) {
		out = append(out, seg)
	}
	return out
}

// hasStatement 判断文本段是否含真实语句（剥掉注释后非空）。
func hasStatement(seg string) bool {
	return strings.TrimSpace(stripComments(seg)) != ""
}
