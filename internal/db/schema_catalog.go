package db

// schema_catalog.go：实际结构查询（catalog）——经 SQLSource 读取 schema_query_*.sql
// 三方言脚本（{table} 占位符替换后执行），把结果归一为统一结构供 diff 引擎比对。
// 与运行时 SQL 同源（外挂覆写自动跟随），表不存在时返回空结果集（上层判定「缺表」）。

import (
	"database/sql"
	"fmt"
	"strings"
)

// CatalogColumn 实际列结构（三方言归一后）。
type CatalogColumn struct {
	Name     string  // 列名
	TypeFull string  // 类型串（含长度/精度，如 varchar(45)）
	Nullable string  // YES / NO
	Default  *string // 默认值表达式（nil = 无默认）
	Extra    string  // 归一化附加标记：auto_increment = 自增
}

// catalogNotExists 判断查询错误是否为「对象不存在」类（catalog 场景按空结果处理）。
// pg 已用 to_regclass 规避、information_schema 天然空集，此兜底仅防御方言差异。
func catalogNotExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") || strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "no such table") || strings.Contains(msg, "unknown table")
}

// queryCatalog 执行 catalog 查询脚本（{table} 占位符替换）并逐行回调。
func (d *DB) queryCatalog(name, table string, scan func(rows *sql.Rows) error) error {
	txt, err := d.SQL(name)
	if err != nil {
		return fmt.Errorf("db: 读取 catalog 脚本 %s 失败: %w", name, err)
	}
	txt = strings.ReplaceAll(txt, "{table}", table)
	rows, err := d.edb.GetSqlDB().Query(txt)
	if err != nil {
		if catalogNotExists(err) {
			return nil // 表不存在 = 空结果集（供上层判定缺表）
		}
		return fmt.Errorf("db: catalog 查询 %s（表 %s）失败: %w", name, table, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CatalogColumns 查询实际列结构（归一化，按定义顺序）。
func (d *DB) CatalogColumns(table string) ([]CatalogColumn, error) {
	var cols []CatalogColumn
	err := d.queryCatalog("schema_query_columns.sql", table, func(rows *sql.Rows) error {
		var c CatalogColumn
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.TypeFull, &c.Nullable, &def, &c.Extra); err != nil {
			return fmt.Errorf("db: catalog 列结果扫描失败: %w", err)
		}
		if def.Valid {
			v := def.String
			c.Default = &v
		}
		cols = append(cols, c)
		return nil
	})
	return cols, err
}

// CatalogIndexes 查询实际索引名集合。
func (d *DB) CatalogIndexes(table string) ([]string, error) {
	var names []string
	err := d.queryCatalog("schema_query_indexes.sql", table, func(rows *sql.Rows) error {
		var n string
		if err := rows.Scan(&n); err != nil {
			return fmt.Errorf("db: catalog 索引结果扫描失败: %w", err)
		}
		names = append(names, n)
		return nil
	})
	return names, err
}

// CatalogTables 查询库内全部基础表名（sqlite 内部表 sqlite_% 已在脚本侧过滤）。
func (d *DB) CatalogTables() ([]string, error) {
	var names []string
	err := d.queryCatalog("schema_query_tables.sql", "", func(rows *sql.Rows) error {
		var n string
		if err := rows.Scan(&n); err != nil {
			return fmt.Errorf("db: catalog 表清单扫描失败: %w", err)
		}
		names = append(names, n)
		return nil
	})
	return names, err
}
