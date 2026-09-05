-- 插入一条 SQL 执行审计记录
INSERT INTO {table} (time, batch_id, seq, sql_text, ok, rows_affected, error, duration_ms, client_ip, source)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
