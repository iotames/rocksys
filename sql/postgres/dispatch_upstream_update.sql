-- 更新负载均衡器（管理面编辑，updated_at 顺带刷新）。
-- 参数：$1=name $2=algo $3=sticky_enabled $4=sticky_cookie $5=enabled $6=remark $7=updated_at(UTC) $8=id
UPDATE {table}
SET name = $1, algo = $2, sticky_enabled = $3, sticky_cookie = $4, enabled = $5, remark = $6, updated_at = $7
WHERE id = $8
