-- mq 插入一条 pending 消息并返回新行自增 id（SQLite 支持 RETURNING，3.35+）。
-- 由 OutboxStore.Insert 在驱动不支持 LastInsertId 时使用；SQLite 正常走 mq_insert.sql + LastInsertId。
INSERT INTO {table} (topic, payload, status, created_at)
VALUES (?, ?, 'pending', ?)
RETURNING id
