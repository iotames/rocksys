-- 表结构同步：实际索引名查询（catalog）。{table} 为运行时表名占位符。
-- 表不存在时返回空结果集（不报错）。
SELECT indexname AS index_name
FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = '{table}'
