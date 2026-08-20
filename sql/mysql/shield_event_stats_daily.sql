-- 按日 × 拦截类别聚合统计（查询时聚合，不建物化表）。参数：from（起始时刻，含）
SELECT DATE_FORMAT(time, '%Y-%m-%d') AS day, block_type, COUNT(*) AS cnt
FROM {table}
WHERE time >= ?
GROUP BY day, block_type
ORDER BY day
