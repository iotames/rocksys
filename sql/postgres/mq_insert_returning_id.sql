-- mq 插入一条 pending 消息并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，OutboxStore.Insert 对本方言专用此脚本。
INSERT INTO {table} (topic, payload, status, created_at)
VALUES ($1, $2, 'pending', $3)
RETURNING id
