-- 节点与均衡器关系表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 多对多关系（关系表带属性 weight/priority）；引用完整性由应用层（adminapi）校验，不建数据库外键。
-- ★ (upstream_id, node_id) 唯一（软删行除外）：由 create_index 部分唯一索引 WHERE deleted_at IS NULL 实现（本方言口径）。
-- ★ 关系行整组替换语义（随均衡器表单整体保存），不设单行 update。
-- ★ priority 枚举（数值稳定，勿改动）：0=高优（默认） 1=备份（高优全挂才启用，即 NGINX backup）。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    upstream_id BIGINT NOT NULL,                 -- 均衡器 → dispatch_upstream.id
    node_id     BIGINT NOT NULL,                 -- 节点 → dispatch_node.id
    weight      INTEGER NOT NULL DEFAULT 1,      -- 权重（正整数默认 1；round_robin 平滑加权用）
    priority    SMALLINT NOT NULL DEFAULT 0,     -- 优先级枚举：0=高优(默认) 1=备份(高优全挂才启用)
    deleted_at  TIMESTAMPTZ,                     -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at  TIMESTAMPTZ NOT NULL,            -- 创建时间（UTC）
    updated_at  TIMESTAMPTZ NOT NULL             -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS '节点与均衡器关系表：多对多关系（带属性 weight/priority）；关系行整组替换语义，不设单行 update；(upstream_id, node_id) 唯一（软删行除外）';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.upstream_id IS '均衡器 → dispatch_upstream.id';
COMMENT ON COLUMN {table}.node_id IS '节点 → dispatch_node.id';
COMMENT ON COLUMN {table}.weight IS '权重（正整数默认 1；round_robin 平滑加权用）';
COMMENT ON COLUMN {table}.priority IS '优先级枚举：0=高优(默认) 1=备份(高优全挂才启用，即 NGINX backup)；数值稳定，勿改动';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
