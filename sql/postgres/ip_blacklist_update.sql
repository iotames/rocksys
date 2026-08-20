-- 更新黑名单条目（管理面：title/block_type/expires_at，updated_at 顺带刷新）。
-- 参数：?1=title ?2=block_type ?3=expires_at（可空） ?4=updated_at(UTC) ?5=id
UPDATE {table}
SET title = $1, block_type = $2, expires_at = $3, updated_at = $4
WHERE id = $5
