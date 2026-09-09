-- 流量趋势（日桶）：access_log（放行，来源标记 'access'）与 shield_event（拦截，标记 'shield'）
-- 两表 UNION ALL 后按日桶 GROUP BY 条件聚合。
-- 参数顺序：from, to, from, to（$1..$4，两分支各一对）。
-- 输出列：bucket（UTC 日桶标签 'YYYY-MM-DD'）、ok_count、blocked_count。
-- 口径说明：ok_count 与 summary.req_ok 同口径（含静态资源、含 4xx/5xx）；blocked_count = 拦截计数。
-- 时间边界 time >= from AND time <= to，各桶之和 = summary 总数（对账依据）。
-- 桶表达式照 shield_event_stats_daily.sql 现状（AT TIME ZONE 'UTC' 转 UTC 墙上时间取日期）。
SELECT bucket,
       SUM(CASE WHEN src = 'access' THEN 1 ELSE 0 END) AS ok_count,
       SUM(CASE WHEN src = 'shield' THEN 1 ELSE 0 END) AS blocked_count
FROM (
  SELECT to_char(time AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS bucket, 'access' AS src
  FROM {table}
  WHERE time >= $1 AND time <= $2
  UNION ALL
  SELECT to_char(time AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS bucket, 'shield' AS src
  FROM {table2}
  WHERE time >= $3 AND time <= $4
) t
GROUP BY bucket
ORDER BY bucket
