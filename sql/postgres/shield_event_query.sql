-- 按条件查询 WAF 拦截明细（id 倒序，最新在前）。
-- 可选条件：block_type=0 表示不过滤；client_ip 空串表示不过滤。
-- 参数顺序：from, to, block_type, block_type, client_ip, client_ip, limit
SELECT time, trace_id, block_type, client_ip, method, path, raw_url, user_agent, host, status_code, rule_hit, req_bytes, extra
FROM {table}
WHERE time >= $1 AND time <= $2
  AND ($3 = 0 OR block_type = $4)
  AND ($5 = '' OR client_ip = $6)
ORDER BY id DESC
LIMIT $7
