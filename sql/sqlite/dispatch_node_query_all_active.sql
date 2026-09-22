-- 查询全部有效节点（Rebuild 构建均衡器时全量拉取：deleted_at IS NULL）。
-- 运行期只读内存快照与探活 registry，不查本表（健康状态不落库）。
SELECT id, name, url, hc_interval_ms, hc_timeout_ms, hc_path, enabled
FROM {table}
WHERE deleted_at IS NULL
ORDER BY id ASC
