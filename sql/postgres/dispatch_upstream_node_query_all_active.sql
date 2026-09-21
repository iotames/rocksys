-- 查询全部有效关系（Rebuild 构建均衡器成员时全量拉取：deleted_at IS NULL）。
SELECT id, upstream_id, node_id, weight, priority
FROM {table}
WHERE deleted_at IS NULL
ORDER BY upstream_id ASC, id ASC
