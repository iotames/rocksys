-- 均衡器列表（管理面，分页 + 过滤 + 排序；软删行不返回）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 id DESC）。
-- 参数：$1=name 模糊（''=不限） $2=name 模糊 $3=enabled（0=不限） $4=enabled $5=limit $6=offset
SELECT id, name, algo, sticky_enabled, sticky_cookie, enabled, remark, created_at, updated_at
FROM {table}
WHERE deleted_at IS NULL
  AND ($1 = '' OR name LIKE '%' || $2 || '%')
  AND ($3 = 0 OR enabled = $4)
ORDER BY {order}
LIMIT $5 OFFSET $6
