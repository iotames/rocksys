-- IP 黑名单表索引（幂等，多条语句由组件拆分逐条执行）。
-- ip 唯一性由建表 UNIQUE 约束保证（重复导入幂等）；以下为过滤/统计/后台清理索引。
CREATE INDEX IF NOT EXISTS idx_{table}_block_type ON {table}(block_type)
CREATE INDEX IF NOT EXISTS idx_{table}_expires_at ON {table}(expires_at)
