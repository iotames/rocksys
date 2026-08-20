-- 管理接口超级管理员表（幂等建表，MySQL 方言）。
-- {table} 为运行时表名占位符，由 adminapi 组件替换（非用户输入，安全）。
-- 密码不存明文：password_hash 为 pbkdf2 哈希（格式 pbkdf2$<iter>$<salt>$<hash>）。
CREATE TABLE IF NOT EXISTS {table} (
    id            BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    username      VARCHAR(64) NOT NULL UNIQUE COMMENT '登录用户名（唯一）',
    password_hash VARCHAR(255) NOT NULL COMMENT '密码哈希（pbkdf2$<iter>$<salt>$<hash>，不存明文）',
    created_at    DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at    DATETIME(3) NOT NULL COMMENT '最近更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='管理接口超级管理员表（登录鉴权用户存储）'
