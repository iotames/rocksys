-- 小黑屋（首页页签）：当前在押的限时封禁条目——未过期（expires_at > now）且未软删；
-- 非永久封禁且正在生效，临近解封的在前（IP_BLACKLIST_PLAN §3.7）。
-- 参数：$1=当前时间(UTC) $2=limit
SELECT id, ip, title, block_type, hit_count, warn_times, created_at, expires_at
FROM {table}
WHERE expires_at IS NOT NULL AND expires_at > $1 AND deleted_at IS NULL
ORDER BY expires_at ASC
LIMIT $2
