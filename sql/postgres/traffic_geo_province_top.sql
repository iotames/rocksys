-- 中国省级分布 Top N：经 geoip_list 关联按 province 聚合计数倒序（去 city 前缀字符串切分）。
-- 参数顺序：from($1), to($2), source($3,$4), limit($5)。
--   source = 'access' → 统计 access_log（放行请求）；否则（'blocked'）→ 统计 shield_event（拦截请求）。
--   limit = 返回条数。
-- 口径：只统计 country_code='CN'（中国地图只渲染境内省区）；province 存 zh-CN 全称（如「广东省」，
--   与前端既有「全称→短名」映射链兼容，禁止存短名）。
-- 输出列：region（省名或空串）、cnt。province 空串原样输出（列保持真实语义不丢量），
--   「市空退省/省空退国」的展示兜底链由读侧映射（region 空串显示「中国」）。
-- 时间边界 time >= from AND time <= to；两分支经 source 条件互斥，只命中其一。
SELECT g.province AS region, COUNT(*) AS cnt
FROM (
  SELECT client_ip FROM {table} WHERE time >= $1 AND time <= $2 AND $3 = 'access'
  UNION ALL
  SELECT client_ip FROM {table2} WHERE time >= $1 AND time <= $2 AND $4 = 'blocked'
) t
JOIN {geo} g ON g.ip = t.client_ip AND g.country_code = 'CN'
GROUP BY g.province
ORDER BY cnt DESC
LIMIT $5
