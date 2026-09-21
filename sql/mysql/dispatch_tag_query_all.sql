-- 查询全部有效标签（筛选下拉全量数据源：deleted_at IS NULL；chip 颜色由前端按名称哈希自动分配）。
SELECT id, name
FROM {table}
WHERE deleted_at IS NULL
ORDER BY name ASC
