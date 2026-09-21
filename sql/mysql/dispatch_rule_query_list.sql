-- 规则列表（管理面，分页 + 过滤 + 排序；软删行不返回）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 match_order ASC）。
-- 参数：?1=关键词（domain/path_value/title 模糊，''=不限） ?2=关键词 ?3=path_type（0=不限） ?4=path_type
--       ?5=enabled（0=不限） ?6=enabled ?7=limit ?8=offset
SELECT id, match_order, domain, path_type, path_value, title, upstream_id, enabled, remark, created_at, updated_at
FROM {table}
WHERE deleted_at IS NULL
  AND (? = '' OR domain LIKE CONCAT('%', ?, '%') OR path_value LIKE CONCAT('%', ?, '%') OR title LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR path_type = ?)
  AND (? = 0 OR enabled = ?)
ORDER BY {order}
LIMIT ? OFFSET ?
