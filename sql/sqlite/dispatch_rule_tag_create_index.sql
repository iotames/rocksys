-- 规则与标签关系表索引（幂等，多条语句由组件拆分逐条执行）。
-- (rule_id, tag_id) 唯一（软删行除外）：复合唯一索引借 NULL 不判重——软删行不计入判重，
-- 活跃行重复由 adminapi 保存查重兜底。
CREATE INDEX IF NOT EXISTS idx_{table}_rule_id ON {table}(rule_id)
CREATE INDEX IF NOT EXISTS idx_{table}_tag_id ON {table}(tag_id)
CREATE UNIQUE INDEX IF NOT EXISTS uk_{table}_pair ON {table}(rule_id, tag_id, deleted_at)
