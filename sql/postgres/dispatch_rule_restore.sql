-- 恢复软删路由规则（清除 deleted_at）。
-- 参数：$1=updated_at(UTC) $2=id
UPDATE {table} SET deleted_at = NULL, updated_at = $1 WHERE id = $2 AND deleted_at IS NOT NULL
