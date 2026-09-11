-- 访问日志表索引（幂等，多条语句由组件拆分逐条执行）
CREATE INDEX IF NOT EXISTS idx_access_log_time ON {table}(time)
CREATE INDEX IF NOT EXISTS idx_access_log_path ON {table}(path)
CREATE INDEX IF NOT EXISTS idx_access_log_status ON {table}(status_code)
CREATE INDEX IF NOT EXISTS idx_access_log_client_ip ON {table}(client_ip)

-- 统计聚合复合索引（traffic summary/geo：时间范围 + status_code / country 组合过滤）
CREATE INDEX IF NOT EXISTS idx_access_log_time_status ON {table}(time, status_code)
CREATE INDEX IF NOT EXISTS idx_access_log_time_country ON {table}(time, country)
