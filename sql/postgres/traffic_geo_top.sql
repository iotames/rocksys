-- 地区分布 Top N：按 country 聚合计数倒序。
-- 参数顺序：from($1), to($2), source($3,$4), limit($5)。
--   source = 'access' → 统计 access_log（放行请求）；否则（'blocked'）→ 统计 shield_event（拦截请求）。
--   limit = 返回条数（照 shield_event_stats_top_ip.sql 现状经参数传入）。
-- 输出列：country（原始 ISO 国家码；空串原样输出、参与排序不丢量，读侧映射显示「未知」）、cnt。
-- 时间边界 time >= from AND time <= to；两分支经 source 条件互斥，只命中其一。
SELECT country, COUNT(*) AS cnt
FROM (
  SELECT country FROM {table} WHERE time >= $1 AND time <= $2 AND $3 = 'access'
  UNION ALL
  SELECT country FROM {table2} WHERE time >= $1 AND time <= $2 AND $4 = 'blocked'
) t
GROUP BY country
ORDER BY cnt DESC
LIMIT $5
