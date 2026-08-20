-- 插入一条 WAF 拦截事件（13 列，extra 为 JSON 扩展）
INSERT INTO {table} (time, trace_id, block_type, client_ip, method, path, raw_url, user_agent, host, status_code, rule_hit, req_bytes, extra)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
