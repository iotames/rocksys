-- mq 插入一条 pending 消息（created_at 由调用方以 time.Time 传入）
INSERT INTO {table} (topic, payload, status, created_at)
VALUES (?, ?, 'pending', ?)
