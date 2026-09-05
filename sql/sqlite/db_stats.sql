-- 数据库空间统计（SQLite 方言）：表清单查询（sqlite_master）。
-- SQLite 无系统级表空间列：逐表占用由端点走 dbstat 按页聚合、总占用取 page_count×page_size，
-- 条数由端点动态 COUNT(*) 精确补齐；本脚本供表清单查询与三方言脚本奇偶校验。
SELECT name AS table_name FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
ORDER BY name
