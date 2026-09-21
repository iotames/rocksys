-- 节点列表总条数（分页用，过滤条件与 dispatch_node_query_list.sql 一致）。
-- 参数：?1=name 模糊（''=不限） ?2=name 模糊 ?3=url 模糊（''=不限） ?4=url 模糊 ?5=enabled（0=不限） ?6=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE deleted_at IS NULL
  AND (? = '' OR name LIKE '%' || ? || '%')
  AND (? = '' OR url LIKE '%' || ? || '%')
  AND (? = 0 OR enabled = ?)
