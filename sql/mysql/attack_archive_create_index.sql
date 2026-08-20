-- 攻击证据归档表索引（多条语句由组件拆分逐条执行）。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
-- "Duplicate key name"——组件对索引创建做幂等容错（该错误忽略）。
-- client_ip / block_type 支撑按来源与拦截类别检索归档。
CREATE INDEX idx_{table}_client_ip ON {table}(client_ip)
CREATE INDEX idx_{table}_block_type ON {table}(block_type)
