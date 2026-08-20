-- 更新白名单条目（管理面：title，updated_at 顺带刷新）。
-- 参数：?1=title ?2=updated_at(UTC) ?3=id
UPDATE {table} SET title = ?, updated_at = ? WHERE id = ?
