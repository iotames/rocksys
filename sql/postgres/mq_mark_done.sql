-- mq 标记消息投递成功
UPDATE {table} SET status = 'done' WHERE id = $1
