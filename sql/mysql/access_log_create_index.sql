-- 访问日志表索引（多条语句由组件拆分逐条执行）。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
-- "Duplicate key name"——组件对索引创建做幂等容错（该错误忽略）。
CREATE INDEX idx_access_log_time ON {table}(time)
CREATE INDEX idx_access_log_path ON {table}(path(255))
CREATE INDEX idx_access_log_status ON {table}(status_code)
