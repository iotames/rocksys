-- 负载均衡器表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- ★ name 唯一（软删行除外）：由 create_index 复合唯一索引 (name, deleted_at) 借 NULL 不判重实现，
--   活跃行重复由 adminapi 保存查重兜底（引用完整性应用层校验惯例，不建数据库外键）。
-- ★ 停用（enabled=0）= 引用它的规则全部 503（WebUI 停用时提示引用数）。
-- ★ algo 均衡策略枚举（数值稳定，勿改动）：1=round_robin（平滑加权轮询，默认） 2=least_conn（最小连接优先）。
CREATE TABLE IF NOT EXISTS {table} (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,    -- 自增主键
    name           TEXT NOT NULL,                        -- 均衡器名称（人类可读、唯一：软删行除外；如「订单服务-会话保持池」）
    algo           INTEGER NOT NULL DEFAULT 1,           -- 均衡策略枚举：1=round_robin(平滑加权轮询，默认) 2=least_conn(最小连接优先)
    sticky_enabled INTEGER NOT NULL DEFAULT 0,           -- 会话保持 1/0；开启后网关自种 Cookie 粘性
    sticky_cookie  TEXT NOT NULL DEFAULT 'rocksys_node', -- Cookie 名；仅 sticky_enabled=1 时生效
    enabled        INTEGER NOT NULL DEFAULT 1,           -- 启用 1/0；停用 = 引用它的规则全部 503
    remark         TEXT NOT NULL DEFAULT '',             -- 备注（人类备注）
    deleted_at     DATETIME,                             -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at     DATETIME NOT NULL,                    -- 创建时间（UTC）
    updated_at     DATETIME NOT NULL                     -- 最后更新时间（UTC）
)
