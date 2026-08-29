-- 按精确 ip 取单条（封禁三态判定/弹窗展示 warn_times 用；取全状态列含软删/过期）。
-- 参数：?1=ip（精确匹配）
SELECT id, ip, title, block_type, hit_count, warn_times, expires_at, deleted_at, created_at, updated_at
FROM {table}
WHERE ip = ?
