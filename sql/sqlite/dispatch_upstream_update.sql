-- 更新负载均衡器（管理面编辑，updated_at 顺带刷新）。
-- 参数：?1=name ?2=algo ?3=sticky_enabled ?4=sticky_cookie ?5=enabled ?6=remark ?7=updated_at(UTC) ?8=id
UPDATE {table}
SET name = ?, algo = ?, sticky_enabled = ?, sticky_cookie = ?, enabled = ?, remark = ?, updated_at = ?
WHERE id = ?
