-- 黑名单列表总条数（分页用，过滤条件与 ip_blacklist_query_list.sql 一致）。
-- 参数：?1=ip 模糊（''=不限） ?2=ip 模糊 ?3=block_type（0=不限） ?4=block_type
--       ?5=valid_only（1=仅有效[未删未过期] 0=全部） ?6=当前时间(UTC)
SELECT COUNT(*) AS total FROM {table}
WHERE (? = '' OR ip LIKE '%' || ? || '%')
  AND (? = 0 OR block_type = ?)
  AND (? = 0 OR (deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)))
