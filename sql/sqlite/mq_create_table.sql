-- mq outbox 建表（幂等）
-- {table} 为运行时表名占位符，由组件构造参数替换（非用户输入，安全）。
CREATE TABLE IF NOT EXISTS {table} (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    topic       TEXT NOT NULL,
    payload     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL
)
