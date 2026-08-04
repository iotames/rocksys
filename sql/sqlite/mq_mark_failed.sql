-- mq 记录一次投递失败：retry_count+1；若达到最大重试次数则转死信（dead），否则置 failed。
-- 参数顺序：最大重试次数, 失败原因, 消息 id
UPDATE {table} SET
    retry_count = retry_count + 1,
    status = CASE WHEN retry_count + 1 >= ? THEN 'dead' ELSE 'failed' END,
    last_error = ?
WHERE id = ?