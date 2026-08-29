-- 表结构同步：库内全部基础表名查询（catalog）。供 F 级「多余表」检测。
-- sqlite 过滤内部表（sqlite_%）。
SELECT name AS table_name FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
ORDER BY name
