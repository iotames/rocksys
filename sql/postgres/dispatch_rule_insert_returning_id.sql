-- 插入一条路由规则并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，对本方言专用此脚本取回 id。
-- 参数：$1=match_order $2=domain $3=path_type $4=path_value $5=title $6=upstream_id $7=enabled $8=remark $9=created_at(UTC) $10=updated_at(UTC)
INSERT INTO {table} (match_order, domain, path_type, path_value, title, upstream_id, enabled, remark, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id
