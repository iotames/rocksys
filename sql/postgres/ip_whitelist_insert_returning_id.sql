-- 插入一条白名单并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，IPListStore.Insert 对本方言专用此脚本。
-- 参数：$1=ip $2=title $3=created_at(UTC) $4=updated_at(UTC)
INSERT INTO {table} (ip, title, created_at, updated_at)
VALUES ($1, $2, $3, $4)
RETURNING id
