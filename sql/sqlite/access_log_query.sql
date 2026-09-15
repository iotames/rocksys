-- 按条件查询访问日志（id 倒序，最新在前），支持状态分组/仅异常/耗时排序与 offset 服务端分页。
-- 地理信息 LEFT JOIN geoip_list 关联返回（country_code/country_name/province/city）；
-- 未命中（IP 未同步入库）返回空串，读侧回退实时解析并做「市→省→国名→未知」兜底显示。
-- 可选条件传空串/0 即不过滤；status_group 传状态码首字符（'2'-'5'）；sort_code：0=时间倒序 1=总耗时降序 2=总耗时升序 3=出网耗时降序 4=出网耗时升序。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id,
--           status_group, status_group, only_error, sort_code, sort_code, limit, offset
SELECT a.id, a.time, a.trace_id, a.tenant_id, a.path, a.method, a.client_ip, a.status_code, a.upstream,
       a.shield_ms, a.biz_ms, a.total_ms, a.egress_ms, a.req_bytes, a.resp_bytes, a.user_agent, a.extra,
       g.country_code, g.country_name, g.province, g.city
FROM {table} a
LEFT JOIN {geo} g ON g.ip = a.client_ip
WHERE a.time >= ? AND a.time <= ?
  AND (? = '' OR a.path = ?)
  AND (? = '' OR a.path LIKE '%' || ? || '%')
  AND (? = '' OR a.trace_id LIKE '%' || ? || '%')
  AND (? = '' OR SUBSTR(CAST(a.status_code AS TEXT), 1, 1) = ?)
  AND (? = 0 OR a.status_code >= 400)
ORDER BY
  CASE ? WHEN 1 THEN a.total_ms WHEN 3 THEN a.egress_ms ELSE -1 END DESC,
  CASE ? WHEN 2 THEN a.total_ms WHEN 4 THEN a.egress_ms ELSE -1 END ASC,
  a.id DESC
LIMIT ? OFFSET ?
