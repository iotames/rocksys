-- 按日 × 拦截类别聚合统计（查询时聚合，不建物化表）。参数：from（起始时刻，含）
-- ★ 三方言统一"按 UTC 日聚合"口径（sqlite 存 UTC 字符串取前 10 位、MySQL 存 UTC 墙上时间字面值取 DATE_FORMAT）：
--   TIMESTAMPTZ 存绝对时刻，必须 AT TIME ZONE 'UTC' 转 UTC 墙上时间再取日期，否则随数据库服务器时区漂移。
SELECT to_char(time AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, block_type, COUNT(*) AS cnt
FROM {table}
WHERE time >= $1
GROUP BY day, block_type
ORDER BY day
