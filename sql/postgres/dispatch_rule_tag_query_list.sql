-- 关系列表（管理面，分页 + 过滤 + 排序；软删行不返回）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 id ASC）。
-- 参数：$1=rule_id（0=不限） $2=rule_id $3=tag_id（0=不限） $4=tag_id $5=limit $6=offset
SELECT id, rule_id, tag_id, created_at, updated_at
FROM {table}
WHERE deleted_at IS NULL
  AND ($1 = 0 OR rule_id = $2)
  AND ($3 = 0 OR tag_id = $4)
ORDER BY {order}
LIMIT $5 OFFSET $6
