-- 攻击证据归档表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 本期仅建表：归档触发/查询逻辑留待后续迭代；
-- 数据不自动清理（审计留存）。
-- 索引见 attack_archive_create_index.sql。
--
-- ★ block_type 拦截类别（复用 shield_event 枚举，数值稳定，勿改动）：
--   1=IP黑名单  2=限流(429)  3=方法不允许  4=请求体超限(413)
--   5=风险路径  6=路径遍历  7=SQL注入  8=XSS  9=爬虫/扫描器UA  10=路径/UA规则deny
--   枚举定义见 plugins/shield/block_type.go（与 Go 源码保持一一对应）。
-- ★ request_headers 完整请求头 JSON（攻击证据，序列化失败以空串占位）。
CREATE TABLE IF NOT EXISTS {table} (
    id               BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    client_ip        VARCHAR(45) NOT NULL DEFAULT '' COMMENT '来源 IP',
    request_uri      VARCHAR(1000) NOT NULL DEFAULT '' COMMENT '请求 URI（含查询串）',
    request_headers  TEXT NOT NULL COMMENT '完整请求头 JSON（攻击证据）',
    block_type       SMALLINT NOT NULL DEFAULT 1 COMMENT '拦截类别 1-10：1=IP黑名单 2=限流 3=方法不允许 4=请求体超限 5=风险路径 6=路径遍历 7=SQL注入 8=XSS 9=爬虫/扫描器UA 10=路径/UA规则deny',
    remark           VARCHAR(64) NOT NULL DEFAULT '' COMMENT '归档备注',
    created_at       DATETIME(3) NOT NULL COMMENT '归档时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='攻击证据归档表：本期仅建表（归档触发/查询留待后续迭代）；数据不自动清理（审计留存）；block_type 1-10 枚举见字段注释'
