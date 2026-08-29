-- IP 黑名单表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 动态 IP 黑名单（数据库持久化，管理面录入/批量导入），与外挂 rules/ip_blacklist.txt 取并集；
-- 请求热路径只读内存快照，本表仅管理操作/启动加载/后台刷新访问（性能红线：热路径零 DB 查询）。
--
-- ★ block_type 拉黑原因类别（复用 shield_event 枚举，数值稳定，勿改动）：
--   0=其他（仅黑名单表）  1=IP黑名单  2=限流(429)  3=方法不允许  4=请求体超限(413)
--   5=风险路径  6=路径遍历  7=SQL注入  8=XSS  9=爬虫/扫描器UA  10=路径/UA规则deny
--   11=人工收录（仅黑名单表）
--   枚举定义见 plugins/shield/block_type.go（与 Go 源码保持一一对应）。
-- ★ 运行时只按 ip 精确/CIDR 匹配；block_type 仅作管理面过滤与统计（该 IP 因哪类请求被识别封禁）。
-- ★ 软删除/过期语义：deleted_at 非 NULL 或 expires_at 已过期（UTC now）的条目不参与匹配。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    ip          TEXT NOT NULL UNIQUE,             -- 精确 IP 或 CIDR（唯一：重复导入幂等拒绝）
    title       TEXT NOT NULL DEFAULT '',         -- 拉黑原因备注（如"Azure 云段扫描器"）
    block_type  SMALLINT NOT NULL DEFAULT 1,      -- 拉黑原因类别（0-11，含义见表头注释；0/11 仅黑名单语境）
    hit_count   INT NOT NULL DEFAULT 0,           -- 命中拦截计数（异步累加，观测/排序用）
    warn_times  INTEGER NOT NULL DEFAULT 0,       -- 封禁次数（人工+自动累计，限时时达 5 次转永久）
    expires_at  TIMESTAMPTZ,                      -- 过期时间（UTC）；NULL=永久，过期条目不参与匹配
    deleted_at  TIMESTAMPTZ,                      -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at  TIMESTAMPTZ NOT NULL,             -- 创建时间（UTC）
    updated_at  TIMESTAMPTZ NOT NULL              -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS 'IP 黑名单表：动态 IP 黑名单（管理面录入/批量导入），与外挂 rules/ip_blacklist.txt 取并集；热路径只读内存快照；block_type 0-11 枚举见列注释';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.ip IS '精确 IP 或 CIDR（唯一：重复导入幂等拒绝）';
COMMENT ON COLUMN {table}.title IS '拉黑原因备注（如"Azure 云段扫描器"）';
COMMENT ON COLUMN {table}.block_type IS '拉黑原因类别 0-11：0=其他(仅黑名单表) 1=IP黑名单 2=限流(429) 3=方法不允许 4=请求体超限(413) 5=风险路径 6=路径遍历 7=SQL注入 8=XSS 9=爬虫/扫描器UA 10=路径/UA规则deny 11=人工收录(仅黑名单表)；仅管理面过滤统计，非运行时匹配依据；枚举定义见 plugins/shield/block_type.go';
COMMENT ON COLUMN {table}.hit_count IS '命中拦截计数（异步累加，观测/排序用）';
COMMENT ON COLUMN {table}.warn_times IS '封禁次数（人工+自动累计，限时时达 5 次转永久）';
COMMENT ON COLUMN {table}.expires_at IS '过期时间（UTC）；NULL=永久，过期条目不参与匹配';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除，不参与匹配';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
