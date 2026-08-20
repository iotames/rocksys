-- 恢复软删黑名单条目（清除 deleted_at）。
-- 参数：?1=updated_at(UTC) ?2=id
UPDATE {table} SET deleted_at = NULL, updated_at = $1 WHERE id = $2
