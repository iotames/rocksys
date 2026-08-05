-- mq outbox 建表（幂等，MySQL 方言）
-- {table} 为运行时表名占位符，由组件构造参数替换（非用户输入，安全）。
-- 注意：MySQL 8.0.13+ 才允许 TEXT 列的 DEFAULT 表达式，故 last_error 用 VARCHAR 简化幂等。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    topic       VARCHAR(255) NOT NULL,
    payload     TEXT NOT NULL,
    status      VARCHAR(16) NOT NULL DEFAULT 'pending',
    retry_count INT NOT NULL DEFAULT 0,
    last_error  VARCHAR(1024) NOT NULL DEFAULT '',
    created_at  VARCHAR(40) NOT NULL
) DEFAULT CHARSET=utf8mb4
