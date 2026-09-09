-- 中国省级分布 Top N：country='CN' 行按 city 前缀（首个 "/" 之前的省名）聚合计数倒序。
-- 参数顺序：from(1), to(2), source(3), from(4), to(5), source(6), limit(7)（source 在两分支各传一次）。
--   source = 'access' → 统计 access_log（放行请求）；否则（'blocked'）→ 统计 shield_event（拦截请求）。
--   limit = 返回条数。
-- 口径：只统计 country='CN'（中国地图只渲染境内省区）；city 列格式为「省/市」（GeoIP zh-CN 本地化），
--   空串/无 "/" 前缀统一计「未知」参与排序不丢量。
-- 输出列：region（省名或「未知」）、cnt。
-- 时间边界 time >= from AND time <= to；两分支经 source 条件互斥，只命中其一。
SELECT region, COUNT(*) AS cnt
FROM (
  SELECT CASE WHEN city = '' THEN '未知' ELSE SUBSTRING_INDEX(city, '/', 1) END AS region
  FROM {table} WHERE time >= ? AND time <= ? AND country = 'CN' AND ? = 'access'
  UNION ALL
  SELECT CASE WHEN city = '' THEN '未知' ELSE SUBSTRING_INDEX(city, '/', 1) END AS region
  FROM {table2} WHERE time >= ? AND time <= ? AND country = 'CN' AND ? = 'blocked'
) t
GROUP BY region
ORDER BY cnt DESC
LIMIT ?
