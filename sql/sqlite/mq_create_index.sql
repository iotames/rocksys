-- mq outbox status 索引（幂等）
CREATE INDEX IF NOT EXISTS idx_{table}_status ON {table}(status)
