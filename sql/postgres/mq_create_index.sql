-- mq outbox status 索引（幂等，PostgreSQL 支持 IF NOT EXISTS）
CREATE INDEX IF NOT EXISTS idx_{table}_status ON {table}(status)
