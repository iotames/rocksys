-- 规则与标签关系表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 多对多纯关联（无属性）；标签随规则表单整体保存（先 upsert 标签实体、再整体替换该规则的关系行）。
-- ★ (rule_id, tag_id) 唯一（软删行除外）：由 create_index 复合唯一索引借 NULL 不判重实现，
--   活跃行重复由 adminapi 保存查重兜底（引用完整性应用层校验惯例，不建数据库外键）。
-- ★ 无引用的标签保留（可复用），不自动清理；标签数据仅在列表/筛选时经 SQL join 读取，不进运行时快照。
CREATE TABLE IF NOT EXISTS {table} (
    id         INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    rule_id    INTEGER NOT NULL,                  -- 规则 → dispatch_rule.id
    tag_id     INTEGER NOT NULL,                  -- 标签 → dispatch_tag.id
    deleted_at DATETIME,                          -- 软删除时间（UTC）；非 NULL 视为已删除
    created_at DATETIME NOT NULL,                 -- 创建时间（UTC）
    updated_at DATETIME NOT NULL                  -- 最后更新时间（UTC）
)
