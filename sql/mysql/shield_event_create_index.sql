-- WAF 拦截事件表索引（多条语句由组件拆分逐条执行）。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
-- "Duplicate key name"——组件对索引创建做幂等容错（该错误忽略）。
-- idx_{table}_time_type 复合索引支撑查询时聚合（按时间范围 + block_type GROUP BY）。
CREATE INDEX idx_{table}_time ON {table}(time)
CREATE INDEX idx_{table}_block_type ON {table}(block_type)
CREATE INDEX idx_{table}_client_ip ON {table}(client_ip)
CREATE INDEX idx_{table}_time_type ON {table}(time, block_type)
