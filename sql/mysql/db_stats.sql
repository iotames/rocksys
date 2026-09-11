-- 数据库空间统计：库内全部基础表的表名/备注/数据占用/索引占用（字节）。
-- 供「服务 → 数据库」页数据表概览。条数不在此查（系统表为估算值，由端点动态 COUNT(*) 精确补齐）。
-- InnoDB 口径：DATA_LENGTH=聚簇索引（即数据本体，含主键），INDEX_LENGTH=二级索引合计；
-- 另有 DATA_FREE（碎片/空闲页）不计入 bytes（口径为「数据+索引」），需要时单独查询。
SELECT TABLE_NAME AS name, TABLE_COMMENT AS comment,
       COALESCE(DATA_LENGTH, 0) AS data_bytes,
       COALESCE(INDEX_LENGTH, 0) AS index_bytes,
       (COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0)) AS bytes
FROM information_schema.tables
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
ORDER BY TABLE_NAME
