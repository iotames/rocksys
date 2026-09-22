-- 路由规则表索引（幂等，多条语句由组件拆分逐条执行）。
-- 快照构建/列表按启用状态 + 匹配序号扫描。
CREATE INDEX IF NOT EXISTS idx_{table}_enabled_deleted_order ON {table}(enabled, deleted_at, match_order)
