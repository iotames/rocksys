-- 插入一条白名单（占位符风格同 ip_whitelist_insert.sql）。
-- 说明：go-sql-driver/mysql 支持 Result.LastInsertId，本脚本在 MySQL 路径下不会被执行
-- （IPListStore.Insert 仅在驱动不支持 LastInsertId 时使用 RETURNING 脚本），
-- 此处保留仅为 sqlite/mysql/postgres 三方言文件集齐平。
-- 参数：?1=ip ?2=title ?3=created_at(UTC) ?4=updated_at(UTC)
INSERT INTO {table} (ip, title, created_at, updated_at)
VALUES (?, ?, ?, ?)
