-- 小黑屋在押条目计数（过滤条件与 ip_blacklist_query_jail.sql 一致）。
-- 参数：$1=当前时间(UTC)
SELECT COUNT(*) AS total FROM {table}
WHERE expires_at IS NOT NULL AND expires_at > $1 AND deleted_at IS NULL
