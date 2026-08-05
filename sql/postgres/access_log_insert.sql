-- 插入一条访问日志（14 个索引列 + extra 负载维度 JSON）
INSERT INTO {table} (time, trace_id, tenant_id, path, method, client_ip, status_code, upstream, shield_ms, biz_ms, total_ms, req_bytes, resp_bytes, extra)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
