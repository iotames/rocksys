-- 规则列表总条数（分页用，过滤条件与 dispatch_rule_query_list.sql 一致）。
-- 参数：$1=include_deleted（0=仅活跃 1=仅已删除） $2=tag（标签名，''=不限） $3=关键词（domain/path_value/title 模糊，''=不限）
--       $4=path_type（0=不限） $5=path_type $6=enabled（0=不限） $7=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE (CASE WHEN $1 = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND ($2 = '' OR id IN (SELECT rt.rule_id FROM {rule_tag} rt JOIN {tags} tg ON tg.id = rt.tag_id WHERE rt.deleted_at IS NULL AND tg.deleted_at IS NULL AND tg.name = $2))
  AND ($3 = '' OR domain LIKE '%' || $3 || '%' OR path_value LIKE '%' || $3 || '%' OR title LIKE '%' || $3 || '%')
  AND ($4 = 0 OR path_type = $5)
  AND ($6 = 0 OR enabled = $7)
