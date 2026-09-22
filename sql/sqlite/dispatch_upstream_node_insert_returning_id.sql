-- 插入一条节点与均衡器关系。
-- 说明：本方言支持 Result.LastInsertId，执行本脚本后由 Go 侧经 Result.LastInsertId 取回自增 id。
-- 参数：?1=upstream_id ?2=node_id ?3=weight ?4=priority ?5=created_at(UTC) ?6=updated_at(UTC)
INSERT INTO {table} (upstream_id, node_id, weight, priority, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
