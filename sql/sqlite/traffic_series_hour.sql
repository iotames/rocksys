-- 流量趋势（小时桶）：access_log（放行，来源标记 'access'）与 shield_event（拦截，标记 'shield'）
-- 两表 UNION ALL 后按小时桶 GROUP BY 条件聚合。
-- 参数顺序：from, to, from, to（两分支各一对，共 4 个 ?）。
-- 输出列：bucket（UTC 小时桶标签 'YYYY-MM-DDTHH:00:00'）、ok_count、blocked_count。
-- 口径说明：ok_count 与 summary.req_ok 同口径（含静态资源、含 4xx/5xx）；blocked_count = 拦截计数。
-- 时间边界 time >= from AND time <= to，各桶之和 = summary 总数（对账依据）。
-- 桶表达式沿 stats_daily 的 substr 日期范式把粒度改到小时：sqlite 驱动将 time.Time 存为
-- RFC3339 字符串（前 16 位恒为 'YYYY-MM-DDTHH'），截取后补 ':00:00'。
SELECT bucket,
       SUM(CASE WHEN src = 'access' THEN 1 ELSE 0 END) AS ok_count,
       SUM(CASE WHEN src = 'shield' THEN 1 ELSE 0 END) AS blocked_count
FROM (
  SELECT SUBSTR(time, 1, 13) || ':00:00' AS bucket, 'access' AS src
  FROM {table}
  WHERE time >= ? AND time <= ?
  UNION ALL
  SELECT SUBSTR(time, 1, 13) || ':00:00' AS bucket, 'shield' AS src
  FROM {table2}
  WHERE time >= ? AND time <= ?
) t
GROUP BY bucket
ORDER BY bucket
