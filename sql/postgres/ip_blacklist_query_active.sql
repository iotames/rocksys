-- 查询全部有效黑名单条目（供内存快照加载：deleted_at IS NULL 且未过期）。
-- 返回 id 与 ip：id 供命中计数异步累加，ip 供精确/CIDR 匹配；运行时只读快照，不查本表。
-- 参数：? = 当前时间（UTC）。
SELECT id, ip FROM {table}
WHERE deleted_at IS NULL AND (expires_at IS NULL OR expires_at > $1)
ORDER BY id ASC
