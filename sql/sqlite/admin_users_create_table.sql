-- 管理接口超级管理员表（幂等建表，SQLite 方言）。
-- {table} 为运行时表名占位符，由 adminapi 组件替换（非用户输入，安全）。
CREATE TABLE IF NOT EXISTS {table} (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
)
