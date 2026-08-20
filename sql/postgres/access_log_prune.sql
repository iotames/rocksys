-- 清理保留期外的访问日志（参数为截止时刻 time.Time，幂等可重复执行）
DELETE FROM {table} WHERE time < $1
