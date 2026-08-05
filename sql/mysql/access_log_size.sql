-- 访问日志表 + 索引占用（字节，information_schema）。
SELECT COALESCE(SUM(data_length + index_length), 0)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name = '{table}'
