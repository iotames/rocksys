-- 路由规则标签表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 标签实体化（GitLab label 同款模型）：全局唯一、重命名一次生效、筛选下拉数据源干净。
-- chip 颜色由前端按名称哈希自动分配，不落库。
-- ★ name 全局唯一、小写（保存时归一；软删行除外）：由 create_index 部分唯一索引 WHERE deleted_at IS NULL 实现（本方言口径）。
CREATE TABLE IF NOT EXISTS {table} (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,          -- 标签名（全局唯一、小写（保存时归一）；软删行除外）
    deleted_at TIMESTAMPTZ,            -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at TIMESTAMPTZ NOT NULL,   -- 创建时间（UTC）
    updated_at TIMESTAMPTZ NOT NULL    -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS '路由规则标签表：标签实体化（GitLab label 同款模型），全局唯一、重命名一次生效；chip 颜色由前端按名称哈希自动分配，不落库';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.name IS '标签名（全局唯一、小写（保存时归一）；软删行除外）';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除，删除时由 adminapi 同步软删其关系行';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
