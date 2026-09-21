-- 恢复软删后端服务器节点（清除 deleted_at）。
-- 参数：?1=updated_at(UTC) ?2=id
UPDATE {table} SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL
