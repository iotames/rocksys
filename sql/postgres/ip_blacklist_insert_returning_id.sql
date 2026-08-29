-- 插入一条黑名单并返回新行自增 id（RETURNING；参数同 ip_blacklist_insert.sql）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，IPListStore.Insert 对本方言专用此脚本。
-- 参数：$1=ip $2=title $3=block_type $4=warn_times $5=expires_at（可空） $6=created_at(UTC) $7=updated_at(UTC)
INSERT INTO {table} (ip, title, block_type, warn_times, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id
