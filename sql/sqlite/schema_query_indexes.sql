-- 表结构同步：实际索引名查询（catalog）。{table} 为运行时表名占位符。
-- 表不存在时返回空结果集（不报错）。
SELECT name AS index_name FROM pragma_index_list('{table}')
