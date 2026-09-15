-- 按条件查询访问日志（耗时排序专用：total/egress 升降序，CASE 排序需物化过滤结果后排序，低频可接受）。
-- 缺省排序（time_desc）走 access_log_query_time.sql（走主键序免临时排序，高频路径）。
-- 地理信息 LEFT JOIN geoip_list 关联返回（country_code/country_name/province/city）；
-- 未命中（IP 未同步入库）返回空串，读侧回退实时解析并做「市→省→国名→未知」兜底显示。
-- 可选条件传空串即不过滤；状态过滤已可索引化：status_lo/status_hi 区间闭区间
-- （不过滤传 0/999999；status_group '2'-'5' → 200-299 等；仅异常 → 400-999999，Go 侧合成）；
-- sort_code：1=总耗时降序 2=总耗时升序 3=出网耗时降序 4=出网耗时升序。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id,
--           status_lo, status_hi, sort_code, sort_code, limit, offset
SELECT a.id, a.time, a.trace_id, a.tenant_id, a.path, a.method, a.client_ip, a.status_code, a.upstream,
       a.shield_ms, a.biz_ms, a.total_ms, a.egress_ms, a.req_bytes, a.resp_bytes, a.user_agent, a.extra,
       g.country_code, g.country_name, g.province, g.city
FROM {table} a
LEFT JOIN {geo} g ON g.ip = a.client_ip
WHERE a.time >= $1 AND a.time <= $2
  AND ($3 = '' OR a.path = $4)
  AND ($5 = '' OR a.path LIKE '%' || $6 || '%')
  AND ($7 = '' OR a.trace_id LIKE '%' || $8 || '%')
  AND a.status_code >= $9 AND a.status_code <= $10
ORDER BY
  -- $11/$12 经 lib/pq 以未知类型下发，PG 对 CASE <param> WHEN <int> 的消解会判为 text
  -- （text = integer 报错），显式 CAST 固化为整数比较（sqlite/mysql 无此问题）。
  CASE CAST($11 AS INTEGER) WHEN 1 THEN a.total_ms WHEN 3 THEN a.egress_ms ELSE -1 END DESC,
  CASE CAST($12 AS INTEGER) WHEN 2 THEN a.total_ms WHEN 4 THEN a.egress_ms ELSE -1 END ASC,
  a.id DESC
LIMIT $13 OFFSET $14
