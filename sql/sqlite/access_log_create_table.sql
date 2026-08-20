-- 访问日志表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 放行请求的访问明细：与 shield_event（拦截记录）分开记录、互不关联。
-- 耗时列单位均为毫秒（ms）。
CREATE TABLE IF NOT EXISTS {table} (
    id          INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    time        DATETIME NOT NULL,                 -- 请求完成时刻（UTC）
    trace_id    TEXT NOT NULL,                     -- 链路 ID（贯穿整条转发链）
    tenant_id   TEXT NOT NULL DEFAULT '',          -- 租户 ID（多租户预留，默认空）
    path        TEXT NOT NULL,                     -- 请求 URL 路径
    method      TEXT NOT NULL,                     -- 请求方法（GET/POST/...）
    client_ip   TEXT NOT NULL DEFAULT '',          -- 客户端 IP（已按 X-Forwarded-For 取真实地址）
    status_code INTEGER NOT NULL,                  -- 上游返回的响应状态码
    upstream    TEXT NOT NULL DEFAULT '',          -- 实际转发的上游地址
    shield_ms   INTEGER NOT NULL DEFAULT 0,        -- L1 防护（shield）环节耗时（ms）
    biz_ms      INTEGER NOT NULL DEFAULT 0,        -- 业务（上游处理）耗时（ms）
    total_ms    INTEGER NOT NULL DEFAULT 0,        -- 请求总耗时（ms）
    req_bytes   INTEGER NOT NULL DEFAULT 0,        -- 请求体字节数
    resp_bytes  INTEGER NOT NULL DEFAULT 0,        -- 响应体字节数
    extra       TEXT NOT NULL DEFAULT '{}'         -- 扩展字段（JSON，向前兼容）
)
