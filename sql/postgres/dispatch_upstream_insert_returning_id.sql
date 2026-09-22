-- 插入一条负载均衡器并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，对本方言专用此脚本取回 id。
-- 参数：$1=name $2=algo $3=sticky_enabled $4=sticky_cookie $5=enabled $6=remark $7=created_at(UTC) $8=updated_at(UTC)
INSERT INTO {table} (name, algo, sticky_enabled, sticky_cookie, enabled, remark, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id
