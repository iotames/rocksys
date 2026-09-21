-- 后端服务器节点基础表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 登记节点资产与体检参数；登记一次、全局去重（探活去重的前提）。
-- ★ url 唯一（软删行除外）：由 create_index 复合唯一索引 (url, deleted_at) 借 NULL 不判重实现，
--   活跃行重复由 adminapi 保存查重兜底（引用完整性应用层校验惯例，不建数据库外键）。
-- ★ 探活语义：hc_path 为空 = 不主动探活，视为健康；健康状态不落库（内存单一事实源，经接口透出）。
CREATE TABLE IF NOT EXISTS {table} (
    id             INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    name           TEXT NOT NULL DEFAULT '',          -- 节点名称（人类可读，空允许）
    url            TEXT NOT NULL,                     -- 节点地址 http(s)://host[:port]（唯一：软删行除外）
    hc_interval_ms INTEGER NOT NULL DEFAULT 20000,    -- 探活周期（ms）；hc_path 为空时不参与探活
    hc_timeout_ms  INTEGER NOT NULL DEFAULT 5000,     -- 探活超时（ms）
    hc_path        TEXT NOT NULL DEFAULT '',          -- 探活路径（以 / 开头）；空 = 不主动探活，视为健康
    enabled        INTEGER NOT NULL DEFAULT 1,        -- 启用 1/0；停用 = 不参与任何均衡器构建与探活
    remark         TEXT NOT NULL DEFAULT '',          -- 备注（人类备注）
    deleted_at     DATETIME,                          -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at     DATETIME NOT NULL,                 -- 创建时间（UTC）
    updated_at     DATETIME NOT NULL                  -- 最后更新时间（UTC）
)
