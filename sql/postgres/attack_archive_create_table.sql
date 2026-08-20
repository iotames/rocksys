-- 攻击证据归档表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 本期仅建表：归档触发/查询逻辑留待后续迭代（见 docs/DEV_HANDBOOK.md §9.4.2）；
-- 数据不自动清理（审计留存）。
--
-- ★ block_type 拦截类别（复用 shield_event 枚举，数值稳定，勿改动）：
--   1=IP黑名单  2=限流(429)  3=方法不允许  4=请求体超限(413)
--   5=风险路径  6=路径遍历  7=SQL注入  8=XSS  9=爬虫/扫描器UA  10=路径/UA规则deny
--   枚举定义见 plugins/shield/block_type.go（与 Go 源码保持一一对应）。
-- ★ request_headers 完整请求头 JSON（攻击证据，序列化失败以空串占位）。
CREATE TABLE IF NOT EXISTS {table} (
    id               BIGSERIAL PRIMARY KEY,
    client_ip        TEXT NOT NULL DEFAULT '',      -- 来源 IP
    request_uri      TEXT NOT NULL DEFAULT '',      -- 请求 URI（含查询串）
    request_headers  TEXT NOT NULL DEFAULT '{}',    -- 完整请求头 JSON（攻击证据）
    block_type       SMALLINT NOT NULL DEFAULT 1,   -- 拦截类别（1-10，含义见表头注释）
    remark           TEXT NOT NULL DEFAULT '',      -- 归档备注
    created_at       TIMESTAMPTZ NOT NULL           -- 归档时间（UTC）
);
COMMENT ON TABLE {table} IS '攻击证据归档表：本期仅建表（归档触发/查询留待后续迭代）；数据不自动清理（审计留存）；block_type 1-10 枚举见列注释';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.client_ip IS '来源 IP';
COMMENT ON COLUMN {table}.request_uri IS '请求 URI（含查询串）';
COMMENT ON COLUMN {table}.request_headers IS '完整请求头 JSON（攻击证据）';
COMMENT ON COLUMN {table}.block_type IS '拦截类别 1-10：1=IP黑名单 2=限流(429) 3=方法不允许 4=请求体超限(413) 5=风险路径 6=路径遍历 7=SQL注入 8=XSS 9=爬虫/扫描器UA 10=路径/UA规则deny；枚举定义见 plugins/shield/block_type.go';
COMMENT ON COLUMN {table}.remark IS '归档备注';
COMMENT ON COLUMN {table}.created_at IS '归档时间（UTC）';
