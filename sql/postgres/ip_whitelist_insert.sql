-- 新增一条白名单（管理面录入）。
-- 参数：?1=ip ?2=title ?3=created_at(UTC) ?4=updated_at(UTC)
-- 唯一约束冲突（ip 已存在）报错，由调用方按"已存在"幂等处理。
INSERT INTO {table} (ip, title, created_at, updated_at)
VALUES ($1, $2, $3, $4)
