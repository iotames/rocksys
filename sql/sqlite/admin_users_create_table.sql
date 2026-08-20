-- 管理接口超级管理员表（幂等建表，SQLite 方言）。
-- {table} 为运行时表名占位符，由 adminapi 组件替换（非用户输入，安全）。
-- 密码不存明文：password_hash 为 pbkdf2 哈希（格式 pbkdf2$<iter>$<salt>$<hash>）。
CREATE TABLE IF NOT EXISTS {table} (
    id            INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    username      TEXT NOT NULL UNIQUE,              -- 登录用户名（唯一）
    password_hash TEXT NOT NULL,                     -- 密码哈希（pbkdf2$<iter>$<salt>$<hash>，不存明文）
    created_at    DATETIME NOT NULL,                 -- 创建时间（UTC）
    updated_at    DATETIME NOT NULL                  -- 最近更新时间（UTC）
)
