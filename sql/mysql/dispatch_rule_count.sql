-- 规则列表总条数（分页用，过滤条件与 dispatch_rule_query_list.sql 一致）。
-- 参数：?1=include_deleted（0=仅活跃 1=仅已删除） ?2=tag（标签名，''=不限） ?3=tag
--       ?4=关键词（''=不限；占位符重复 4 次） ?5=关键词 ?6=关键词 ?7=关键词
--       ?8=path_type（0=不限） ?9=path_type ?10=enabled（0=不限） ?11=enabled
SELECT COUNT(*) AS total FROM {table}
WHERE (CASE WHEN ? = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND (? = '' OR id IN (SELECT rt.rule_id FROM {rule_tag} rt JOIN {tags} tg ON tg.id = rt.tag_id WHERE rt.deleted_at IS NULL AND tg.deleted_at IS NULL AND tg.name = ?))
  AND (? = '' OR domain LIKE CONCAT('%', ?, '%') OR path_value LIKE CONCAT('%', ?, '%') OR title LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR path_type = ?)
  AND (? = 0 OR enabled = ?)
