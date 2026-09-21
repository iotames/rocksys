-- 更新路由规则（管理面编辑，updated_at 顺带刷新；标签关系随表单经 dispatch_rule_tag 整组替换）。
-- 参数：?1=match_order ?2=domain ?3=path_type ?4=path_value ?5=title ?6=upstream_id ?7=enabled ?8=remark ?9=updated_at(UTC) ?10=id
UPDATE {table}
SET match_order = ?, domain = ?, path_type = ?, path_value = ?, title = ?, upstream_id = ?, enabled = ?, remark = ?, updated_at = ?
WHERE id = ?
