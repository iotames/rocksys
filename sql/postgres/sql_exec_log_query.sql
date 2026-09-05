-- 查询 SQL 执行审计（id 倒序最新在前），服务端分页。
-- 参数顺序：limit, offset
SELECT id, time, batch_id, seq, sql_text, ok, rows_affected, error, duration_ms, client_ip, source
FROM {table}
ORDER BY id DESC
LIMIT $1 OFFSET $2
