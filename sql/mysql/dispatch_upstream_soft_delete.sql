-- 软删除负载均衡器（deleted_at = now；管理面可恢复，见 restore；存在未软删规则引用时由 adminapi 拒绝删除）。
-- 参数：?1=deleted_at(UTC) ?2=updated_at(UTC) ?3=id
UPDATE {table} SET deleted_at = ?, updated_at = ? WHERE id = ?
