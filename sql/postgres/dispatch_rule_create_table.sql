-- 路由规则表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- ★ 匹配语义：match_order 1–999 升序、命中即停；域名默认兜底规则建议 999。
-- ★ domain 空 = 匹配任意域名；非空 = 精确匹配、剥端口、转小写（无通配）；
--   保存时归一落库（防外部改库存入未归一值永不命中），Rebuild 构建期归一进快照。
-- ★ path_type 枚举（数值稳定，勿改动）：1=前缀（段对齐） 2=精确 3=模式（:param/*）。
--   path_value 以 / 开头，按 path_type 解释；/ + 前缀 = 全路径兜底。
-- ★ 标签不在本表存列，经 dispatch_tag / dispatch_rule_tag 两表关联。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    match_order INTEGER NOT NULL,                -- 匹配序号（1-999 升序、命中即停；域名默认兜底建议 999）
    domain      TEXT NOT NULL DEFAULT '',        -- 域名（可选）；空=匹配任意域名；非空=精确匹配、剥端口、转小写（保存时归一落库）
    path_type   SMALLINT NOT NULL DEFAULT 1,     -- 路径类型枚举：1=前缀(段对齐) 2=精确 3=模式(:param/*)
    path_value  TEXT NOT NULL,                   -- 路径值（以 / 开头，按 path_type 解释；/ + 前缀 = 全路径兜底）
    title       TEXT NOT NULL DEFAULT '',        -- 规则标题（人类可读，空允许）
    upstream_id BIGINT NOT NULL,                 -- 均衡器 → dispatch_upstream.id（命中规则的转发目标）
    enabled     SMALLINT NOT NULL DEFAULT 1,     -- 启用 1/0；停用行不参与匹配，保留配置
    remark      TEXT NOT NULL DEFAULT '',        -- 备注（人类备注）
    deleted_at  TIMESTAMPTZ,                     -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at  TIMESTAMPTZ NOT NULL,            -- 创建时间（UTC）
    updated_at  TIMESTAMPTZ NOT NULL             -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS '路由规则表：match_order 升序命中即停；domain 精确匹配（保存时归一落库）；path_type 1=前缀/2=精确/3=模式；标签经 dispatch_tag/dispatch_rule_tag 关联';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.match_order IS '匹配序号（1-999 升序、命中即停；域名默认兜底规则建议 999）';
COMMENT ON COLUMN {table}.domain IS '域名（可选）；空=匹配任意域名；非空=精确匹配、剥端口、转小写（无通配，保存时归一落库）';
COMMENT ON COLUMN {table}.path_type IS '路径类型枚举：1=前缀(段对齐) 2=精确 3=模式(:param/*)；数值稳定，勿改动';
COMMENT ON COLUMN {table}.path_value IS '路径值（以 / 开头，按 path_type 解释；/ + 前缀 = 全路径兜底）';
COMMENT ON COLUMN {table}.title IS '规则标题（人类可读，空允许）';
COMMENT ON COLUMN {table}.upstream_id IS '均衡器 → dispatch_upstream.id（命中规则的转发目标）';
COMMENT ON COLUMN {table}.enabled IS '启用 1/0；停用行不参与匹配，保留配置';
COMMENT ON COLUMN {table}.remark IS '备注（人类备注）';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
