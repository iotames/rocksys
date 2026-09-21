-- 更新路由规则标签（重命名，一次生效；updated_at 顺带刷新）。
-- 参数：?1=name ?2=updated_at(UTC) ?3=id
UPDATE {table} SET name = ?, updated_at = ? WHERE id = ?
