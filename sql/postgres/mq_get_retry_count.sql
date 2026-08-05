-- mq 查询消息当前重试次数
SELECT retry_count FROM {table} WHERE id = $1
