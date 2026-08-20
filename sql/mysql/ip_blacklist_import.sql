-- 批量导入黑名单（幂等：ip 已存在则忽略，不更新、不报错）。
-- 参数：?1=ip ?2=title ?3=block_type ?4=created_at(UTC) ?5=updated_at(UTC)
INSERT IGNORE INTO {table} (ip, title, block_type, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
