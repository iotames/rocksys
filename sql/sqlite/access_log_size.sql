-- 访问日志表 + 索引占用（字节）。
-- dbstat 为编译期启用的虚拟表（modernc.org/sqlite 默认支持），精确到表/索引页。
SELECT COALESCE(SUM(pgsize), 0) FROM dbstat
WHERE name = '{table}' OR name LIKE 'idx_{table}%'
