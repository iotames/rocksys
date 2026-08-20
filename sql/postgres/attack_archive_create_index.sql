-- 攻击证据归档表索引（幂等，多条语句由组件拆分逐条执行）。
-- client_ip / block_type 支撑按来源与拦截类别检索归档。
CREATE INDEX IF NOT EXISTS idx_{table}_client_ip ON {table}(client_ip)
CREATE INDEX IF NOT EXISTS idx_{table}_block_type ON {table}(block_type)
