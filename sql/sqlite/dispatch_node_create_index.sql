-- 后端服务器节点基础表索引（幂等，多条语句由组件拆分逐条执行）。
-- url 唯一（软删行除外）：复合唯一索引 (url, deleted_at) 借 NULL 不判重——软删行不计入判重，
-- 活跃行重复由 adminapi 保存查重兜底。
CREATE UNIQUE INDEX IF NOT EXISTS uk_{table}_url ON {table}(url, deleted_at)
