-- 数据库空间统计：库内全部基础表的表名/备注/占用空间（数据+索引合计，字节）。
-- 供「服务 → 数据库」页数据表概览。条数不在此查（系统表为估算值，由端点动态 COUNT(*) 精确补齐）。
SELECT TABLE_NAME AS name, TABLE_COMMENT AS comment,
       (DATA_LENGTH + INDEX_LENGTH) AS bytes
FROM information_schema.tables
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
ORDER BY TABLE_NAME
