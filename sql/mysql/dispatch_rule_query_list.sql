-- 规则列表（管理面，分页 + 过滤 + 排序；include_deleted 控制软删行：0=仅活跃行 1=仅已删除行）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 match_order ASC）。
-- 参数：?1=include_deleted（0=仅活跃 1=仅已删除） ?2=tag（标签名，''=不限） ?3=tag
--       ?4=关键词（domain/path_value/title 模糊，''=不限；占位符重复 4 次） ?5=关键词 ?6=关键词 ?7=关键词
--       ?8=path_type（0=不限） ?9=path_type ?10=enabled（0=不限） ?11=enabled ?12=limit ?13=offset
SELECT id, match_order, domain, path_type, path_value, title, upstream_id, enabled, remark, created_at, updated_at, deleted_at
FROM {table}
WHERE (CASE WHEN ? = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND (? = '' OR id IN (SELECT rt.rule_id FROM {rule_tag} rt JOIN {tags} tg ON tg.id = rt.tag_id WHERE rt.deleted_at IS NULL AND tg.deleted_at IS NULL AND tg.name = ?))
  AND (? = '' OR domain LIKE CONCAT('%', ?, '%') OR path_value LIKE CONCAT('%', ?, '%') OR title LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR path_type = ?)
  AND (? = 0 OR enabled = ?)
ORDER BY {order}
LIMIT ? OFFSET ?
