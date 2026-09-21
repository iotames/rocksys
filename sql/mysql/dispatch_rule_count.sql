-- 规则列表总条数（分页用，过滤条件与 dispatch_rule_query_list.sql 一致）。
-- 参数：?1=关键词（''=不限） ?2=关键词 ?3=path_type（0=不限） ?4=path_type ?5=enabled（0=不限） ?6=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE deleted_at IS NULL
  AND (? = '' OR domain LIKE CONCAT('%', ?, '%') OR path_value LIKE CONCAT('%', ?, '%') OR title LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR path_type = ?)
  AND (? = 0 OR enabled = ?)
