-- 新增一条黑名单（管理面录入；block_type 默认 1 由 Go 侧填充）。
-- 参数：?1=ip ?2=title ?3=block_type ?4=expires_at（可空） ?5=created_at(UTC) ?6=updated_at(UTC)
-- 唯一约束冲突（ip 已存在）报错，由调用方按"已存在"幂等处理。
INSERT INTO {table} (ip, title, block_type, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
