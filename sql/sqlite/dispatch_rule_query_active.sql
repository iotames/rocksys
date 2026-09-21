-- 查询全部启用规则（Rebuild 构建路由快照拉启用行：enabled=1 且 deleted_at IS NULL）。
-- 匹配顺序 match_order 升序、命中即停；运行期只读内存快照，不查本表。
SELECT id, match_order, domain, path_type, path_value, title, upstream_id
FROM {table}
WHERE enabled = 1 AND deleted_at IS NULL
ORDER BY match_order ASC, id ASC
