-- 访问日志表索引（幂等，多条语句由组件拆分逐条执行）
CREATE INDEX IF NOT EXISTS idx_access_log_time ON {table}(time)
CREATE INDEX IF NOT EXISTS idx_access_log_path ON {table}(path)
CREATE INDEX IF NOT EXISTS idx_access_log_status ON {table}(status_code)
