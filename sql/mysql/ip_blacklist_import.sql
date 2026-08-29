-- 批量导入黑名单（幂等：ip 已存在则忽略，不更新、不报错）。
-- 参数：?1=ip ?2=title ?3=block_type ?4=warn_times（导入初始 0） ?5=created_at(UTC) ?6=updated_at(UTC)
INSERT IGNORE INTO {table} (ip, title, block_type, warn_times, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
