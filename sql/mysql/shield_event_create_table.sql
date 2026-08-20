-- WAF 拦截事件明细表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- block_type 枚举见 plugins/shield/block_type.go（1-10，SMALLINT 数值稳定）。索引见 shield_event_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    time        DATETIME(3) NOT NULL,        -- 拦截时刻（Go 侧传 time.Time）
    trace_id    VARCHAR(255) NOT NULL DEFAULT '',
    block_type  SMALLINT NOT NULL,           -- 拦截类别
    client_ip   VARCHAR(64) NOT NULL DEFAULT '',
    method      VARCHAR(16) NOT NULL DEFAULT '',
    path        VARCHAR(2048) NOT NULL,      -- URL 路径
    raw_url     VARCHAR(2048) NOT NULL DEFAULT '', -- 含查询串的原始 URL（攻击特征常在此）
    user_agent  VARCHAR(512) NOT NULL DEFAULT '',
    host        VARCHAR(255) NOT NULL DEFAULT '',
    status_code INT NOT NULL DEFAULT 0,      -- 拦截响应码（403/413/429）
    rule_hit    VARCHAR(255) NOT NULL DEFAULT '', -- 命中的规则/特征名
    req_bytes   BIGINT NOT NULL DEFAULT 0,
    extra       TEXT NOT NULL                -- 扩展（referer / x_forwarded_for 等，JSON）
) DEFAULT CHARSET=utf8mb4
