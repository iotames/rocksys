-- 查询全部有效均衡器（Rebuild 构建路由快照时全量拉取：deleted_at IS NULL）。
-- 运行期只读内存快照，不查本表。
SELECT id, name, algo, sticky_enabled, sticky_cookie, enabled
FROM {table}
WHERE deleted_at IS NULL
ORDER BY id ASC
