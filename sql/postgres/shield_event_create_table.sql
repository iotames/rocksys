-- WAF 拦截事件明细表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 拦截与放行分开记录：本表仅存被拦截的请求（obs 的 access_log 存放行请求，互不关联）。
--
-- ★ block_type 拦截类别（SMALLINT 数值稳定，勿改动，WebUI 与统计脚本依赖）：
--   1=IP黑名单  2=限流(429)  3=方法不允许  4=请求体超限(413)
--   5=风险路径  6=路径遍历  7=SQL注入  8=XSS  9=爬虫/扫描器UA  10=路径/UA规则deny
--   枚举定义见 plugins/shield/block_type.go（与 Go 源码保持一一对应）。
-- ★ rule_hit 命中规则/特征名：sql_pattern / xss_pattern / path_traversal / risk_path /
--   crawler_ua / ip_blacklist / rate_limit / method_whitelist / max_body_size 等。
-- ★ status_code 拦截响应码：403（黑名单/风险路径/遍历/SQL注入/XSS/爬虫UA/方法不允许/规则deny）、
--   413（请求体超限）、429（限流）。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    time        TIMESTAMPTZ NOT NULL,             -- 拦截时刻（UTC，Go 侧传 time.Time）
    trace_id    TEXT NOT NULL DEFAULT '',         -- 链路 ID（仅 shield_event 内部追溯，不与 access_log 关联）
    block_type  SMALLINT NOT NULL,                -- 拦截类别（1-10，含义见表头注释）
    client_ip   TEXT NOT NULL DEFAULT '',         -- 攻击来源 IP（已按 X-Forwarded-For 取真实客户端地址）
    method      TEXT NOT NULL DEFAULT '',         -- 请求方法（GET/POST/...）
    path        TEXT NOT NULL DEFAULT '',         -- URL 路径
    raw_url     TEXT NOT NULL DEFAULT '',         -- 含查询串的原始 URL（攻击特征常在此）
    user_agent  TEXT NOT NULL DEFAULT '',         -- 客户端 User-Agent（爬虫识别依据）
    host        TEXT NOT NULL DEFAULT '',         -- 请求 Host
    status_code INT NOT NULL DEFAULT 0,           -- 拦截响应码（403/413/429，含义见表头注释）
    rule_hit    TEXT NOT NULL DEFAULT '',         -- 命中规则/特征名（见表头注释）
    req_bytes   BIGINT NOT NULL DEFAULT 0,        -- 请求体字节数（Content-Length）
    extra       TEXT NOT NULL DEFAULT '{}'        -- 扩展字段（referer / x_forwarded_for 等，JSON）
);
COMMENT ON TABLE {table} IS 'WAF 拦截事件明细表：仅存被拦截的请求，与 access_log（放行请求）分开记录；block_type 1-10 枚举见列注释';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.time IS '拦截时刻（UTC，Go 侧传 time.Time）';
COMMENT ON COLUMN {table}.trace_id IS '链路 ID（仅 shield_event 内部追溯，不与 access_log 关联）';
COMMENT ON COLUMN {table}.block_type IS '拦截类别 1-10：1=IP黑名单 2=限流(429) 3=方法不允许 4=请求体超限(413) 5=风险路径 6=路径遍历 7=SQL注入 8=XSS 9=爬虫/扫描器UA 10=路径/UA规则deny；枚举定义见 plugins/shield/block_type.go';
COMMENT ON COLUMN {table}.client_ip IS '攻击来源 IP（已按 X-Forwarded-For 取真实客户端地址）';
COMMENT ON COLUMN {table}.method IS '请求方法（GET/POST/...）';
COMMENT ON COLUMN {table}.path IS 'URL 路径';
COMMENT ON COLUMN {table}.raw_url IS '含查询串的原始 URL（攻击特征常在此）';
COMMENT ON COLUMN {table}.user_agent IS '客户端 User-Agent（爬虫识别依据）';
COMMENT ON COLUMN {table}.host IS '请求 Host';
COMMENT ON COLUMN {table}.status_code IS '拦截响应码：403（黑名单/风险路径/遍历/SQL注入/XSS/爬虫UA/方法不允许/规则deny）、413（请求体超限）、429（限流）';
COMMENT ON COLUMN {table}.rule_hit IS '命中规则/特征名：sql_pattern/xss_pattern/path_traversal/risk_path/crawler_ua/ip_blacklist/rate_limit/method_whitelist/max_body_size 等';
COMMENT ON COLUMN {table}.req_bytes IS '请求体字节数（Content-Length）';
COMMENT ON COLUMN {table}.extra IS '扩展字段（referer / x_forwarded_for 等，JSON）';
