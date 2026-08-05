-- 管理接口超级管理员表（幂等建表，MySQL 方言）。
-- {table} 为运行时表名占位符，由 adminapi 组件替换（非用户输入，安全）。
CREATE TABLE IF NOT EXISTS {table} (
    id            BIGINT AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    created_at    VARCHAR(40) NOT NULL,
    updated_at    VARCHAR(40) NOT NULL
) DEFAULT CHARSET=utf8mb4
