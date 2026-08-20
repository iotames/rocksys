-- 管理接口超级管理员表（幂等建表，PostgreSQL 方言）。
-- {table} 为运行时表名占位符，由 adminapi 组件替换（非用户输入，安全）。
-- 密码不存明文：password_hash 为 pbkdf2 哈希（格式 pbkdf2$<iter>$<salt>$<hash>）。
CREATE TABLE IF NOT EXISTS {table} (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL
);
COMMENT ON TABLE {table} IS '管理接口超级管理员表（登录鉴权用户存储）';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.username IS '登录用户名（唯一）';
COMMENT ON COLUMN {table}.password_hash IS '密码哈希（pbkdf2$<iter>$<salt>$<hash>，不存明文）';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最近更新时间（UTC）';
