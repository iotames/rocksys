-- 表结构同步：实际列结构查询（catalog）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 期望结构（sql/<dbtype>/*_create_table.sql）vs 实际结构（本查询）比对，见 docs/DB_SCHEMA_SYNC_PLAN.md。
-- 统一输出别名：name / type_full / is_nullable(YES|NO) / col_default(NULL=未声明) / extra(auto_increment=自增)。
-- 表不存在时返回空结果集（不报错），供上层判定「缺表」。
SELECT COLUMN_NAME AS name,
       COLUMN_TYPE AS type_full,
       IS_NULLABLE AS is_nullable,
       COLUMN_DEFAULT AS col_default,
       IFNULL(EXTRA, '') AS extra
FROM information_schema.columns
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '{table}'
ORDER BY ORDINAL_POSITION
