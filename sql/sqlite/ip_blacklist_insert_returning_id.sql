-- 插入一条黑名单（占位符风格同 ip_blacklist_insert.sql）。
-- 说明：本方言支持 Result.LastInsertId，IPListStore.Insert 走 ip_blacklist_insert.sql；
-- 此脚本仅为三方言文件集齐平保留（与 postgres/ip_blacklist_insert_returning_id.sql 对应），
-- 在本方言路径下不会被执行。
INSERT INTO {table} (ip, title, block_type, warn_times, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
