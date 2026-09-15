-- 插入一条访问日志（16 个索引列 + extra 负载维度 JSON；地理信息经 geoip_list 关联，不再逐行存）
INSERT INTO {table} (time, trace_id, tenant_id, path, method, client_ip, status_code, upstream, shield_ms, biz_ms, total_ms, egress_ms, req_bytes, resp_bytes, user_agent, extra)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
