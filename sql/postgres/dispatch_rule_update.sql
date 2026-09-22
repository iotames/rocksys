-- 更新路由规则（管理面编辑，updated_at 顺带刷新；标签关系随表单经 dispatch_rule_tag 整组替换）。
-- 参数：$1=match_order $2=domain $3=path_type $4=path_value $5=title $6=upstream_id $7=enabled $8=remark $9=updated_at(UTC) $10=id
UPDATE {table}
SET match_order = $1, domain = $2, path_type = $3, path_value = $4, title = $5, upstream_id = $6, enabled = $7, remark = $8, updated_at = $9
WHERE id = $10
