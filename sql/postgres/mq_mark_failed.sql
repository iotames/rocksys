-- mq 记录一次投递失败：retry_count+1；若达到最大重试次数则转死信（dead），否则置 failed。
-- 参数顺序：最大重试次数, 失败原因, 消息 id
-- 注意：status 赋值必须放在 retry_count 递增之前。MySQL 的 UPDATE SET 从左到右求值
-- （与标准 SQL 不同），若先递增 retry_count，CASE 读到的是已更新的值，会提前一拍转死信；
-- 将 status 前置后，MySQL 与标准语义（CASE 基于旧值判断）一致。PostgreSQL 为标准语义，
-- 顺序无关，此写法对其同样正确。
UPDATE {table} SET
    status = CASE WHEN retry_count + 1 >= $1 THEN 'dead' ELSE 'failed' END,
    retry_count = retry_count + 1,
    last_error = $2
WHERE id = $3
