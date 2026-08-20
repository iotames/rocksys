-- 黑名单列表（管理面，分页 + 过滤）。
-- 参数：?1=ip 模糊（''=不限） ?2=ip 模糊 ?3=block_type（0=不限） ?4=block_type
--       ?5=valid_only（1=仅有效[未删未过期] 0=全部） ?6=当前时间(UTC) ?7=limit ?8=offset
SELECT id, ip, title, block_type, hit_count, expires_at, deleted_at, created_at, updated_at
FROM {table}
WHERE ($1 = '' OR ip LIKE '%' || $2 || '%')
  AND ($3 = 0 OR block_type = $4)
  AND ($5 = 0 OR (deleted_at IS NULL AND (expires_at IS NULL OR expires_at > $6)))
ORDER BY id DESC
LIMIT $7 OFFSET $8
