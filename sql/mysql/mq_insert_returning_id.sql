-- mq 插入一条 pending 消息（占位符风格同 mq_insert.sql）。
-- 说明：go-sql-driver/mysql 支持 Result.LastInsertId，本脚本在 MySQL 路径下不会被执行
-- （OutboxStore.Insert 仅在驱动不支持 LastInsertId 时使用 RETURNING 脚本），
-- 此处保留仅为 sqlite/mysql/postgres 三方言文件集齐平。
INSERT INTO {table} (topic, payload, status, created_at)
VALUES (?, ?, 'pending', ?)
