-- WAF 拦截事件明细表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- block_type 枚举见 plugins/shield/block_type.go（1-10，SMALLINT 数值稳定）。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    time        TIMESTAMPTZ NOT NULL,        -- 拦截时刻（Go 侧传 time.Time）
    trace_id    TEXT NOT NULL DEFAULT '',    -- 链路 ID（仅 shield_event 内部追溯，不与 access_log 关联）
    block_type  SMALLINT NOT NULL,           -- 拦截类别
    client_ip   TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL DEFAULT '',    -- URL 路径
    raw_url     TEXT NOT NULL DEFAULT '',    -- 含查询串的原始 URL（攻击特征常在此）
    user_agent  TEXT NOT NULL DEFAULT '',
    host        TEXT NOT NULL DEFAULT '',
    status_code INT NOT NULL DEFAULT 0,      -- 拦截响应码（403/413/429）
    rule_hit    TEXT NOT NULL DEFAULT '',    -- 命中的规则/特征名
    req_bytes   BIGINT NOT NULL DEFAULT 0,
    extra       TEXT NOT NULL DEFAULT '{}'   -- 扩展（referer / x_forwarded_for 等，JSON）
)
