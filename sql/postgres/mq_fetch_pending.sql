-- mq 取出待投递/待重试消息（status in pending,failed），按 id 升序，最多 limit 条
SELECT id, topic, payload, status, retry_count, created_at
FROM {table}
WHERE status IN ('pending', 'failed')
ORDER BY id ASC
LIMIT $1
