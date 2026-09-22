-- 节点与均衡器关系表索引（多条语句由组件拆分逐条执行）。
-- 注意：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报
-- "Duplicate key name"——组件对索引创建做幂等容错（该错误忽略）。
-- (upstream_id, node_id) 唯一（软删行除外）：复合唯一索引借 NULL 不判重——软删行不计入判重，
-- 活跃行重复由 adminapi 保存查重兜底。
CREATE INDEX idx_{table}_upstream_id ON {table}(upstream_id)
CREATE INDEX idx_{table}_node_id ON {table}(node_id)
CREATE UNIQUE INDEX uk_{table}_pair ON {table}(upstream_id, node_id, deleted_at)
