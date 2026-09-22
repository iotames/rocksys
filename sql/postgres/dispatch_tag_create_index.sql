-- 路由规则标签表索引（幂等，多条语句由组件拆分逐条执行）。
-- name 唯一（软删行除外）：本方言经部分唯一索引 WHERE deleted_at IS NULL 实现；
-- 活跃行重复由 adminapi 保存查重兜底。
CREATE UNIQUE INDEX IF NOT EXISTS uk_{table}_name ON {table}(name) WHERE deleted_at IS NULL
