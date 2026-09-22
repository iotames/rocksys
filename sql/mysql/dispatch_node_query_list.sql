-- 节点列表（管理面，分页 + 过滤 + 排序；include_deleted 控制软删行：0=仅活跃行 1=仅已删除行）。
-- 排序经 {order} 占位符由 Go 侧白名单映射注入（非用户输入直拼，杜绝注入面；缺省 id DESC）。
-- 参数：?1=include_deleted（0=仅活跃 1=仅已删除） ?2=name 模糊（''=不限） ?3=name 模糊
--       ?4=url 模糊（''=不限） ?5=url 模糊 ?6=enabled（0=不限） ?7=enabled ?8=limit ?9=offset
SELECT id, name, url, hc_interval_ms, hc_timeout_ms, hc_path, enabled, remark, created_at, updated_at, deleted_at
FROM {table}
WHERE (CASE WHEN ? = 1 THEN deleted_at IS NOT NULL ELSE deleted_at IS NULL END)
  AND (? = '' OR name LIKE CONCAT('%', ?, '%'))
  AND (? = '' OR url LIKE CONCAT('%', ?, '%'))
  AND (? = 0 OR enabled = ?)
ORDER BY {order}
LIMIT ? OFFSET ?
