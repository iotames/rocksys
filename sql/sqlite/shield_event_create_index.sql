-- WAF 拦截事件表索引（幂等，多条语句由组件拆分逐条执行）。
-- idx_{table}_time_type 复合索引支撑查询时聚合（按时间范围 + block_type GROUP BY）。
CREATE INDEX IF NOT EXISTS idx_{table}_time ON {table}(time)
CREATE INDEX IF NOT EXISTS idx_{table}_block_type ON {table}(block_type)
CREATE INDEX IF NOT EXISTS idx_{table}_client_ip ON {table}(client_ip)
CREATE INDEX IF NOT EXISTS idx_{table}_time_type ON {table}(time, block_type)
