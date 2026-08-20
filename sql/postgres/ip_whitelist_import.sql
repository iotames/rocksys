-- 批量导入白名单（幂等：ip 已存在则忽略，不更新、不报错）。
-- 参数：?1=ip ?2=title ?3=created_at(UTC) ?4=updated_at(UTC)
INSERT INTO {table} (ip, title, created_at, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (ip) DO NOTHING
