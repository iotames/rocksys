-- 按条件查询访问日志（time 升序，id 递增）。可选条件传空串即不过滤。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id, limit
SELECT time, trace_id, tenant_id, path, method, client_ip, status_code, upstream,
       shield_ms, biz_ms, total_ms, req_bytes, resp_bytes, extra
FROM {table}
WHERE time >= $1 AND time <= $2
  AND ($3 = '' OR path = $4)
  AND ($5 = '' OR path LIKE '%' || $6 || '%')
  AND ($7 = '' OR trace_id LIKE '%' || $8 || '%')
ORDER BY id DESC
LIMIT $9
