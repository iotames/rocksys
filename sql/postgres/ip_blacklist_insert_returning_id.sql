-- 插入一条黑名单并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，IPListStore.Insert 对本方言专用此脚本。
-- 参数：$1=ip $2=title $3=block_type $4=expires_at（可空） $5=created_at(UTC) $6=updated_at(UTC)
INSERT INTO {table} (ip, title, block_type, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id
