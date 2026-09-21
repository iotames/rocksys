-- 后端服务器节点基础表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 登记节点资产与体检参数；登记一次、全局去重（探活去重的前提）。
-- ★ url 唯一（软删行除外）：由 create_index 部分唯一索引 WHERE deleted_at IS NULL 实现（本方言口径）。
-- ★ 探活语义：hc_path 为空 = 不主动探活，视为健康；健康状态不落库（内存单一事实源，经接口透出）。
CREATE TABLE IF NOT EXISTS {table} (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL DEFAULT '',       -- 节点名称（人类可读，空允许）
    url            TEXT NOT NULL,                  -- 节点地址 http(s)://host[:port]（唯一：软删行除外）
    hc_interval_ms INTEGER NOT NULL DEFAULT 20000, -- 探活周期（ms）；hc_path 为空时不参与探活
    hc_timeout_ms  INTEGER NOT NULL DEFAULT 5000,  -- 探活超时（ms）
    hc_path        TEXT NOT NULL DEFAULT '',       -- 探活路径（以 / 开头）；空 = 不主动探活，视为健康
    enabled        SMALLINT NOT NULL DEFAULT 1,    -- 启用 1/0；停用 = 不参与任何均衡器构建与探活
    remark         TEXT NOT NULL DEFAULT '',       -- 备注（人类备注）
    deleted_at     TIMESTAMPTZ,                    -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at     TIMESTAMPTZ NOT NULL,           -- 创建时间（UTC）
    updated_at     TIMESTAMPTZ NOT NULL            -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS '后端服务器节点基础表：登记节点资产与体检参数；登记一次、全局去重（探活去重的前提）；url 唯一（软删行除外）；健康状态不落库（内存单一事实源）';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.name IS '节点名称（人类可读，空允许）';
COMMENT ON COLUMN {table}.url IS '节点地址 http(s)://host[:port]（唯一：软删行除外）';
COMMENT ON COLUMN {table}.hc_interval_ms IS '探活周期（ms），默认 20000；hc_path 为空时不参与探活';
COMMENT ON COLUMN {table}.hc_timeout_ms IS '探活超时（ms），默认 5000';
COMMENT ON COLUMN {table}.hc_path IS '探活路径（以 / 开头）；空 = 不主动探活，视为健康';
COMMENT ON COLUMN {table}.enabled IS '启用 1/0；停用 = 不参与任何均衡器构建与探活';
COMMENT ON COLUMN {table}.remark IS '备注（人类备注）';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
