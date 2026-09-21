-- 均衡器列表总条数（分页用，过滤条件与 dispatch_upstream_query_list.sql 一致）。
-- 参数：$1=name 模糊（''=不限） $2=name 模糊 $3=enabled（0=不限） $4=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE deleted_at IS NULL
  AND ($1 = '' OR name LIKE '%' || $2 || '%')
  AND ($3 = 0 OR enabled = $4)
