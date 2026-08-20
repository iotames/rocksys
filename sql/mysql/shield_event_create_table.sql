-- WAF 拦截事件明细表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 拦截与放行分开记录：本表仅存被拦截的请求（obs 的 access_log 存放行请求，互不关联）。
-- 索引见 shield_event_create_index.sql。
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
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    time        DATETIME(3) NOT NULL COMMENT '拦截时刻（UTC，Go 侧传 time.Time）',
    trace_id    VARCHAR(255) NOT NULL DEFAULT '' COMMENT '链路 ID（仅 shield_event 内部追溯，不与 access_log 关联）',
    block_type  SMALLINT NOT NULL COMMENT '拦截类别 1-10：1=IP黑名单 2=限流 3=方法不允许 4=请求体超限 5=风险路径 6=路径遍历 7=SQL注入 8=XSS 9=爬虫/扫描器UA 10=路径/UA规则deny',
    client_ip   VARCHAR(64) NOT NULL DEFAULT '' COMMENT '攻击来源 IP（已按 X-Forwarded-For 取真实客户端地址）',
    method      VARCHAR(16) NOT NULL DEFAULT '' COMMENT '请求方法（GET/POST/...）',
    path        VARCHAR(2048) NOT NULL COMMENT 'URL 路径',
    raw_url     VARCHAR(2048) NOT NULL DEFAULT '' COMMENT '含查询串的原始 URL（攻击特征常在此）',
    user_agent  VARCHAR(512) NOT NULL DEFAULT '' COMMENT '客户端 User-Agent（爬虫识别依据）',
    host        VARCHAR(255) NOT NULL DEFAULT '' COMMENT '请求 Host',
    status_code INT NOT NULL DEFAULT 0 COMMENT '拦截响应码：403（黑名单/风险路径/遍历/SQL注入/XSS/爬虫UA/方法不允许/规则deny）、413（请求体超限）、429（限流）',
    rule_hit    VARCHAR(255) NOT NULL DEFAULT '' COMMENT '命中规则/特征名：sql_pattern/xss_pattern/path_traversal/risk_path/crawler_ua 等',
    req_bytes   BIGINT NOT NULL DEFAULT 0 COMMENT '请求体字节数（Content-Length）',
    extra       TEXT NOT NULL COMMENT '扩展字段（referer / x_forwarded_for 等，JSON）'
) DEFAULT CHARSET=utf8mb4 COMMENT='WAF 拦截事件明细表：仅存被拦截的请求，与 access_log（放行请求）分开记录；block_type 1-10 枚举见表字段注释'
