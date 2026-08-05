-- 访问日志表占用（字节，pg_total_relation_size 含表/索引/TOAST）。
SELECT pg_total_relation_size('{table}')
