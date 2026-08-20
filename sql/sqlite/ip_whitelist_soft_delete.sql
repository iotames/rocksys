-- 软删除白名单条目（deleted_at = now；管理面可恢复，见 restore）。
-- 参数：?1=deleted_at(UTC) ?2=updated_at(UTC) ?3=id
UPDATE {table} SET deleted_at = ?, updated_at = ? WHERE id = ?
