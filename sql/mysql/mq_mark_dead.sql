-- mq 强制标记消息为死信
UPDATE {table} SET status = 'dead' WHERE id = ?
