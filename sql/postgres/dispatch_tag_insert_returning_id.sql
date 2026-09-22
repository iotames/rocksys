-- 插入一条路由规则标签并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，对本方言专用此脚本取回 id。
-- 参数：$1=name $2=created_at(UTC) $3=updated_at(UTC)
INSERT INTO {table} (name, created_at, updated_at)
VALUES ($1, $2, $3)
RETURNING id
