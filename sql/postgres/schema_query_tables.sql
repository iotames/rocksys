-- 表结构同步：库内全部基础表名查询（catalog）。供 F 级「多余表」检测。
-- sqlite 过滤内部表（sqlite_%）。
SELECT tablename AS table_name
FROM pg_tables
WHERE schemaname = current_schema()
ORDER BY tablename
