-- mq outbox status 索引。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报 "Duplicate key name"
-- ——mq 组件对索引创建做幂等容错（该错误忽略），与 obs 组件索引逻辑一致。
CREATE INDEX idx_{table}_status ON {table}(status)
