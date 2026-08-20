-- 白名单列表总条数（分页用，过滤条件与 ip_whitelist_query_list.sql 一致）。
-- 参数：?1=ip 模糊（''=不限） ?2=ip 模糊 ?3=valid_only（1=仅有效[未删] 0=全部含软删）
SELECT COUNT(*) AS total FROM {table}
WHERE (? = '' OR ip LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR deleted_at IS NULL)
