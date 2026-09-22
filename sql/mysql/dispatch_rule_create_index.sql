-- 路由规则表索引（多条语句由组件拆分逐条执行）。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
-- "Duplicate key name"——组件对索引创建做幂等容错（该错误忽略）。
-- 快照构建/列表按启用状态 + 匹配序号扫描。
CREATE INDEX idx_{table}_enabled_deleted_order ON {table}(enabled, deleted_at, match_order)
