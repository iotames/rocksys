-- 更新后端服务器节点（管理面编辑，updated_at 顺带刷新）。
-- 参数：$1=name $2=url $3=hc_interval_ms $4=hc_timeout_ms $5=hc_path $6=enabled $7=remark $8=updated_at(UTC) $9=id
UPDATE {table}
SET name = $1, url = $2, hc_interval_ms = $3, hc_timeout_ms = $4, hc_path = $5, enabled = $6, remark = $7, updated_at = $8
WHERE id = $9
