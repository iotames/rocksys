-- 规则与标签关系表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 多对多纯关联（无属性）；标签随规则表单整体保存（先 upsert 标签实体、再整体替换该规则的关系行）。
-- ★ (rule_id, tag_id) 唯一（软删行除外）：由 create_index 复合唯一索引借 NULL 不判重实现（本方言口径）。
-- ★ 无引用的标签保留（可复用），不自动清理；标签数据仅在列表/筛选时经 SQL join 读取，不进运行时快照。
-- 索引见 dispatch_rule_tag_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id         BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    rule_id    BIGINT NOT NULL COMMENT '规则 → dispatch_rule.id',
    tag_id     BIGINT NOT NULL COMMENT '标签 → dispatch_tag.id',
    deleted_at DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除；标签删除时由 adminapi 同步软删其全部关系行',
    created_at DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='规则与标签关系表：多对多纯关联（无属性）；标签随规则表单整体保存（整体替换关系行）；(rule_id, tag_id) 唯一（软删行除外）'
