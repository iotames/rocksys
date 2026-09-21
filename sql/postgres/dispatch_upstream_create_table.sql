-- 负载均衡器表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- ★ name 唯一（软删行除外）：由 create_index 部分唯一索引 WHERE deleted_at IS NULL 实现（本方言口径）。
-- ★ 停用（enabled=0）= 引用它的规则全部 503（WebUI 停用时提示引用数）。
-- ★ algo 均衡策略枚举（数值稳定，勿改动）：1=round_robin（平滑加权轮询，默认） 2=least_conn（最小连接优先）。
CREATE TABLE IF NOT EXISTS {table} (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL,                  -- 均衡器名称（人类可读、唯一：软删行除外；如「订单服务-会话保持池」）
    algo           SMALLINT NOT NULL DEFAULT 1,    -- 均衡策略枚举：1=round_robin(平滑加权轮询，默认) 2=least_conn(最小连接优先)
    sticky_enabled SMALLINT NOT NULL DEFAULT 0,    -- 会话保持 1/0；开启后网关自种 Cookie 粘性
    sticky_cookie  TEXT NOT NULL DEFAULT 'rocksys_node', -- Cookie 名；仅 sticky_enabled=1 时生效
    enabled        SMALLINT NOT NULL DEFAULT 1,    -- 启用 1/0；停用 = 引用它的规则全部 503
    remark         TEXT NOT NULL DEFAULT '',       -- 备注（人类备注）
    deleted_at     TIMESTAMPTZ,                    -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at     TIMESTAMPTZ NOT NULL,           -- 创建时间（UTC）
    updated_at     TIMESTAMPTZ NOT NULL            -- 最后更新时间（UTC）
);
COMMENT ON TABLE {table} IS '负载均衡器表：algo 1=round_robin(默认)/2=least_conn；会话保持经网关自种 Cookie；停用 = 引用它的规则全部 503；name 唯一（软删行除外）';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.name IS '均衡器名称（人类可读、唯一：软删行除外；如「订单服务-会话保持池」）';
COMMENT ON COLUMN {table}.algo IS '均衡策略枚举：1=round_robin(平滑加权轮询，默认) 2=least_conn(最小连接优先)；数值稳定，勿改动';
COMMENT ON COLUMN {table}.sticky_enabled IS '会话保持 1/0，默认 0；开启后网关自种 Cookie 粘性';
COMMENT ON COLUMN {table}.sticky_cookie IS 'Cookie 名，默认 rocksys_node；仅 sticky_enabled=1 时生效';
COMMENT ON COLUMN {table}.enabled IS '启用 1/0；停用 = 引用它的规则全部 503（WebUI 停用时提示引用数）';
COMMENT ON COLUMN {table}.remark IS '备注（人类备注）';
COMMENT ON COLUMN {table}.deleted_at IS '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
COMMENT ON COLUMN {table}.updated_at IS '最后更新时间（UTC）';
