-- 规则列表（管理面，分页 + 过滤 + 排序；include_deleted 控制软删行：0=仅活跃行 1=仅已删除行）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 match_order ASC）。
-- 参数：$1=include_deleted（0=仅活跃 1=仅已删除） $2=tag（标签名，''=不限） $3=关键词（domain/path_value/title 模糊，''=不限）
--       $4=path_type（0=不限） $5=path_type $6=enabled（0=不限） $7=enabled $8=limit $9=offset
--       （PG 复用编号：tag 占位符为 $2；关键词占位符 $3 重复 2 次）
SELECT id, match_order, domain, path_type, path_value, title, upstream_id, enabled, remark, created_at, updated_at, deleted_at
FROM {table}
WHERE (CASE WHEN $1 = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND ($2 = '' OR id IN (SELECT rt.rule_id FROM {rule_tag} rt JOIN {tags} tg ON tg.id = rt.tag_id WHERE rt.deleted_at IS NULL AND tg.deleted_at IS NULL AND tg.name = $2))
  AND ($3 = '' OR domain LIKE '%' || $3 || '%' OR path_value LIKE '%' || $3 || '%' OR title LIKE '%' || $3 || '%')
  AND ($4 = 0 OR path_type = $5)
  AND ($6 = 0 OR enabled = $7)
ORDER BY {order}
LIMIT $8 OFFSET $9
