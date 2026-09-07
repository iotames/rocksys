-- 小黑屋（首页页签）：当前在押的全部封禁条目——未过期（永久封禁视为不过期）且未软删；
-- 限时封禁临近解封的在前，永久封禁（expires_at 为 NULL）殿后（IP_BLACKLIST_PLAN §3.7）。
-- 参数：$1=当前时间(UTC) $2=limit
SELECT id, ip, title, block_type, hit_count, warn_times, created_at, expires_at
FROM {table}
WHERE (expires_at IS NULL OR expires_at > $1) AND deleted_at IS NULL
ORDER BY expires_at ASC NULLS LAST
LIMIT $2
