-- 中国省级分布 Top N：country='CN' 行按 city 前缀（首个 "/" 之前的省名）聚合计数倒序。
-- 参数顺序：from(1), to(2), source(3), from(4), to(5), source(6), limit(7)（source 在两分支各传一次）。
--   source = 'access' → 统计 access_log（放行请求）；否则（'blocked'）→ 统计 shield_event（拦截请求）。
--   limit = 返回条数。
-- 口径：只统计 country='CN'（中国地图只渲染境内省区）；city 列格式为「省/市」（GeoIP zh-CN 本地化）。
--   city 为空串时 region 原样输出空串（列保持真实语义不丢量），「市空退省/省空退国」的
--   展示兜底链由读侧映射（省前缀本身已承载"市空退省"——列值为「省/市」，市缺即「省」）。
-- 输出列：region（省名或空串）、cnt。
-- 时间边界 time >= from AND time <= to；两分支经 source 条件互斥，只命中其一。
SELECT region, COUNT(*) AS cnt
FROM (
  SELECT substr(city, 1, instr(city || '/', '/') - 1) AS region
  FROM {table} WHERE time >= ? AND time <= ? AND country = 'CN' AND ? = 'access'
  UNION ALL
  SELECT substr(city, 1, instr(city || '/', '/') - 1) AS region
  FROM {table2} WHERE time >= ? AND time <= ? AND country = 'CN' AND ? = 'blocked'
) t
GROUP BY region
ORDER BY cnt DESC
LIMIT ?
