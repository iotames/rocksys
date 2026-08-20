-- 按日 × 拦截类别聚合统计（查询时聚合，不建物化表）。参数：from（起始时刻，含）
-- 说明：sqlite 驱动将 time.Time 存为 RFC3339 字符串（含 'T' 与纳秒），
-- strftime 对该格式解析失败返回 NULL，故用 substr 截取日期前缀（ISO 格式前 10 位恒为 YYYY-MM-DD）。
SELECT substr(time, 1, 10) AS day, block_type, COUNT(*) AS cnt
FROM {table}
WHERE time >= ?
GROUP BY day, block_type
ORDER BY day
