-- 白名单列表（管理面，分页 + 过滤）。
-- 参数：?1=ip 模糊（''=不限） ?2=ip 模糊 ?3=valid_only（1=仅有效[未删] 0=全部含软删）
--       ?4=limit ?5=offset
SELECT id, ip, title, deleted_at, created_at, updated_at
FROM {table}
WHERE (? = '' OR ip LIKE '%' || ? || '%')
  AND (? = 0 OR deleted_at IS NULL)
ORDER BY id DESC
LIMIT ? OFFSET ?
