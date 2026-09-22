-- 均衡器列表总条数（分页用，过滤条件与 dispatch_upstream_query_list.sql 一致）。
-- 参数：?1=include_deleted（0=仅活跃 1=仅已删除） ?2=name 模糊（''=不限） ?3=name 模糊
--       ?4=enabled（0=不限） ?5=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE (CASE WHEN ? = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND (? = '' OR name LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR enabled = ?)
