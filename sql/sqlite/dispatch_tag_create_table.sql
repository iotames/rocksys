-- 路由规则标签表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 标签实体化（GitLab label 同款模型）：全局唯一、重命名一次生效、筛选下拉数据源干净。
-- chip 颜色由前端按名称哈希自动分配，不落库。
-- ★ name 全局唯一、小写（保存时归一；软删行除外）：由 create_index 复合唯一索引借 NULL 不判重实现，
--   活跃行重复由 adminapi 保存查重兜底（引用完整性应用层校验惯例，不建数据库外键）。
CREATE TABLE IF NOT EXISTS {table} (
    id         INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    name       TEXT NOT NULL,                     -- 标签名（全局唯一、小写（保存时归一）；软删行除外）
    deleted_at DATETIME,                          -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at DATETIME NOT NULL,                 -- 创建时间（UTC）
    updated_at DATETIME NOT NULL                  -- 最后更新时间（UTC）
)
