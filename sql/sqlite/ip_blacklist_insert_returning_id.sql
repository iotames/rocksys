-- 插入一条黑名单并返回新行自增 id（SQLite 支持 RETURNING，3.35+）。
-- 由 IPListStore.Insert 在驱动不支持 LastInsertId 时使用；SQLite 正常走 ip_blacklist_insert.sql + LastInsertId。
-- 参数：?1=ip ?2=title ?3=block_type ?4=expires_at（可空） ?5=created_at(UTC) ?6=updated_at(UTC)
INSERT INTO {table} (ip, title, block_type, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id
