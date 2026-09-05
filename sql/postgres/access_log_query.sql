-- 按条件查询访问日志（id 倒序，最新在前），支持状态分组/仅异常/耗时排序与 offset 服务端分页。
-- 可选条件传空串/0 即不过滤；status_group 传状态码首字符（'2'-'5'）；sort_code：0=时间倒序 1=总耗时降序 2=总耗时升序 3=出网耗时降序 4=出网耗时升序。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id,
--           status_group, status_group, only_error, sort_code, sort_code, limit, offset
SELECT id, time, trace_id, tenant_id, path, method, client_ip, status_code, upstream,
       shield_ms, biz_ms, total_ms, egress_ms, req_bytes, resp_bytes, extra
FROM {table}
WHERE time >= $1 AND time <= $2
  AND ($3 = '' OR path = $4)
  AND ($5 = '' OR path LIKE '%' || $6 || '%')
  AND ($7 = '' OR trace_id LIKE '%' || $8 || '%')
  AND ($9 = '' OR SUBSTR(CAST(status_code AS TEXT), 1, 1) = $10)
  AND ($11 = 0 OR status_code >= 400)
ORDER BY
  CASE $12 WHEN 1 THEN total_ms WHEN 3 THEN egress_ms ELSE -1 END DESC,
  CASE $13 WHEN 2 THEN total_ms WHEN 4 THEN egress_ms ELSE -1 END ASC,
  id DESC
LIMIT $14 OFFSET $15
