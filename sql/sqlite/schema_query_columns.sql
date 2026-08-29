-- 表结构同步：实际列结构查询（catalog）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 期望结构（sql/<dbtype>/*_create_table.sql）vs 实际结构（本查询）比对，见 docs/DB_SCHEMA_SYNC_PLAN.md。
-- 统一输出别名：name / type_full / is_nullable(YES|NO) / col_default(NULL=未声明) / extra(auto_increment=自增)。
-- 表不存在时返回空结果集（不报错），供上层判定「缺表」。
SELECT name AS name,
       type AS type_full,
       CASE WHEN "notnull" = 1 THEN 'NO' ELSE 'YES' END AS is_nullable,
       dflt_value AS col_default,
       CASE WHEN pk = 1 AND upper(type) = 'INTEGER' THEN 'auto_increment' WHEN pk = 1 THEN 'primary_key' ELSE '' END AS extra
FROM pragma_table_info('{table}')
