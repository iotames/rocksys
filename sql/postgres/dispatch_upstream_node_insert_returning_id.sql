-- 插入一条节点与均衡器关系并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，对本方言专用此脚本取回 id。
-- 参数：$1=upstream_id $2=node_id $3=weight $4=priority $5=created_at(UTC) $6=updated_at(UTC)
INSERT INTO {table} (upstream_id, node_id, weight, priority, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id
