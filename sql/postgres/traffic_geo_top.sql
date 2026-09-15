-- 地区分布 Top N：经 geoip_list 关联按 country_code 聚合计数倒序（去逐行 country 列依赖）。
-- 参数顺序：from($1), to($2), source($3,$4), limit($5)。
--   source = 'access' → 统计 access_log（放行请求）；否则（'blocked'）→ 统计 shield_event（拦截请求）。
--   limit = 返回条数（照 shield_event_stats_top_ip.sql 现状经参数传入）。
-- 输出列：country（ISO 码，聚合口径；未同步 IP 的空串原样输出、参与排序不丢量，读侧显示「未知」）、
--         country_name（本地化国名，取组内任一非空）、cnt。
-- 时间边界 time >= from AND time <= to；两分支经 source 条件互斥，只命中其一。
SELECT g.country_code AS country, MAX(g.country_name) AS country_name, COUNT(*) AS cnt
FROM (
  SELECT client_ip FROM {table} WHERE time >= $1 AND time <= $2 AND $3 = 'access'
  UNION ALL
  SELECT client_ip FROM {table2} WHERE time >= $1 AND time <= $2 AND $4 = 'blocked'
) t
LEFT JOIN {geo} g ON g.ip = t.client_ip
GROUP BY g.country_code
ORDER BY cnt DESC
LIMIT $5
