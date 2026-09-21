-- 规则列表总条数（分页用，过滤条件与 dispatch_rule_query_list.sql 一致）。
-- 参数：?1=include_deleted（0=仅活跃 1=仅已删除） ?2=关键词（''=不限） ?3=关键词
--       ?4=path_type（0=不限） ?5=path_type ?6=enabled（0=不限） ?7=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE (CASE WHEN ? = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND (? = '' OR domain LIKE CONCAT('%', ?, '%') OR path_value LIKE CONCAT('%', ?, '%') OR title LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR path_type = ?)
  AND (? = 0 OR enabled = ?)
