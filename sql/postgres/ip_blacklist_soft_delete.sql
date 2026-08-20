-- 软删除黑名单条目（deleted_at = now；管理面可恢复，见 restore）。
-- 参数：?1=deleted_at(UTC) ?2=updated_at(UTC) ?3=id
UPDATE {table} SET deleted_at = $1, updated_at = $2 WHERE id = $3
