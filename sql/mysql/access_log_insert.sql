-- 插入一条访问日志（18 个索引列 + extra 负载维度 JSON）
INSERT INTO {table} (time, trace_id, tenant_id, path, method, client_ip, status_code, upstream, shield_ms, biz_ms, total_ms, egress_ms, req_bytes, resp_bytes, user_agent, country, city, extra)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
