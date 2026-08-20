-- 查询全部有效白名单条目（供内存快照加载：deleted_at IS NULL）。
-- 返回 id 与 ip：ip 供精确/CIDR 匹配（白名单优先短路放行）；运行时只读快照，不查本表。
SELECT id, ip FROM {table}
WHERE deleted_at IS NULL
ORDER BY id ASC
