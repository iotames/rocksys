-- 节点列表（管理面，分页 + 过滤 + 排序；软删行不返回）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 id DESC）。
-- 参数：?1=name 模糊（''=不限） ?2=name 模糊 ?3=url 模糊（''=不限） ?4=url 模糊
--       ?5=enabled（0=不限） ?6=enabled ?7=limit ?8=offset
SELECT id, name, url, hc_interval_ms, hc_timeout_ms, hc_path, enabled, remark, created_at, updated_at
FROM {table}
WHERE deleted_at IS NULL
  AND (? = '' OR name LIKE CONCAT('%', ?, '%'))
  AND (? = '' OR url LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR enabled = ?)
ORDER BY {order}
LIMIT ? OFFSET ?
