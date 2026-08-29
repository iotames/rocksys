-- 新增一条黑名单（管理面录入/封禁入库；block_type 缺省 11 人工收录由 Go 侧填充）。
-- 参数：$1=ip $2=title $3=block_type $4=warn_times（封禁次数：封禁入库=1，普通录入/导入=0）
--       $5=expires_at（可空，NULL=永久） $6=created_at(UTC) $7=updated_at(UTC)
-- 唯一约束冲突（ip 已存在）报错，由调用方按"已存在"幂等处理。
INSERT INTO {table} (ip, title, block_type, warn_times, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
