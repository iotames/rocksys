-- 黑名单列表（管理面，分页 + 过滤 + 排序）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（见 ip_list_store.go blacklistSortWhitelist，
-- 非用户输入直拼，杜绝注入面；缺省 id DESC）。
-- 参数：?1=ip 模糊（''=不限） ?2=ip 模糊 ?3=block_type（0=不限） ?4=block_type
--       ?5=valid_only（1=仅有效[未删未过期] 0=全部） ?6=当前时间(UTC) ?7=limit =offset
SELECT id, ip, title, block_type, hit_count, warn_times, expires_at, deleted_at, created_at, updated_at
FROM {table}
WHERE (? = '' OR ip LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR block_type = ?)
  AND (? = 0 OR (deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)))
ORDER BY {order}
LIMIT ? OFFSET ?
