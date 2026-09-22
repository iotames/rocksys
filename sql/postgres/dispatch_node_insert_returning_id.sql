-- 插入一条后端服务器节点并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，对本方言专用此脚本取回 id。
-- 参数：$1=name $2=url $3=hc_interval_ms $4=hc_timeout_ms $5=hc_path $6=enabled $7=remark $8=created_at(UTC) $9=updated_at(UTC)
INSERT INTO {table} (name, url, hc_interval_ms, hc_timeout_ms, hc_path, enabled, remark, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id
