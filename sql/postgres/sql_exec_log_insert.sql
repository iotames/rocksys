-- 插入一条 SQL 执行审计记录
INSERT INTO {table} (time, batch_id, seq, sql_text, ok, rows_affected, error, duration_ms, client_ip, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
