-- IP 黑名单表索引（多条语句由组件拆分逐条执行）。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
-- "Duplicate key name"——组件对索引创建做幂等容错（该错误忽略）。
-- ip 唯一性由建表 UNIQUE KEY 保证（重复导入幂等）；以下为过滤/统计/后台清理索引。
CREATE INDEX idx_{table}_block_type ON {table}(block_type)
CREATE INDEX idx_{table}_expires_at ON {table}(expires_at)
