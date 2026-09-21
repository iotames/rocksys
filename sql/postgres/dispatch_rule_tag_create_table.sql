-- 规则与标签关系表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 多对多纯关联（无属性）；标签随规则表单整体保存（先 upsert 标签实体、再整体替换该规则的关系行）。
-- ★ (rule_id, tag_id) 唯一（软删行除外）：由 create_index 部分唯一索引 WHERE deleted_at IS NULL 实现（本方言口径）。
-- ★ 无引用的标签保留（可复用），不自动清理；标签数据仅在列表/筛选时经 SQL join 读取，不进运行时快照。
CREATE TABLE IF NOT EXISTS {table} (
    id         BIGSERIAL PRIMARY KEY,
    rule_id    BIGINT NOT NULL,        -- 规则 → dispatch_rule.id
    tag_id     BIGINT NOT NULL,        -- 标签 → dispatch_tag.id
    deleted_at TIMESTAMPTZ,            -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at TIMESTAMPTZ NOT NULL,   -- 创建时间（UTC）
    updated_at TIMESTAMPTZ NOT NULL    -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS '规则与标签关系表：多对多纯关联（无属性）；标签随规则表单整体保存（整体替换关系行）；(rule_id, tag_id) 唯一（软删行除外）';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.rule_id IS '规则 → dispatch_rule.id';
COMMENT ON COLUMN {table}.tag_id IS '标签 → dispatch_tag.id';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除；标签删除时由 adminapi 同步软删其全部关系行';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
