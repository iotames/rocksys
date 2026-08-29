-- 表结构同步：库内全部基础表名查询（catalog）。供 F 级「多余表」检测。
-- sqlite 过滤内部表（sqlite_%）。
SELECT TABLE_NAME AS table_name
FROM information_schema.tables
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
ORDER BY TABLE_NAME
