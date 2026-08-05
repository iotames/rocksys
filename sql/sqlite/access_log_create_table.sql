-- 访问日志表（幂等建表）。{table} 为运行时表名占位符，由组件构造参数替换（非用户输入，安全）。
CREATE TABLE IF NOT EXISTS {table} (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    time        TEXT NOT NULL,
    trace_id    TEXT NOT NULL,
    tenant_id   TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL,
    method      TEXT NOT NULL,
    client_ip   TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL,
    upstream    TEXT NOT NULL DEFAULT '',
    shield_ms   INTEGER NOT NULL DEFAULT 0,
    biz_ms      INTEGER NOT NULL DEFAULT 0,
    total_ms    INTEGER NOT NULL DEFAULT 0,
    req_bytes   INTEGER NOT NULL DEFAULT 0,
    resp_bytes  INTEGER NOT NULL DEFAULT 0,
    extra       TEXT NOT NULL DEFAULT '{}'
)
