-- 表结构同步：实际列结构查询（catalog）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 期望结构（sql/<dbtype>/*_create_table.sql）vs 实际结构（本查询）比对，见 docs/DB_SCHEMA_SYNC_PLAN.md。
-- 统一输出别名：name / type_full / is_nullable(YES|NO) / col_default(NULL=未声明) / extra(auto_increment=自增)。
-- 表不存在时返回空结果集（不报错），供上层判定「缺表」。
SELECT a.attname AS name,
       format_type(a.atttypid, a.atttypmod) AS type_full,
       CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END AS is_nullable,
       pg_get_expr(d.adbin, d.adrelid) AS col_default,
       CASE WHEN pg_get_expr(d.adbin, d.adrelid) LIKE '%nextval%' THEN 'auto_increment' ELSE '' END AS extra
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = to_regclass('{table}') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum
