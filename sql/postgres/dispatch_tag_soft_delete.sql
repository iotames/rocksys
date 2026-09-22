-- 软删除路由规则标签（deleted_at = now；删除时由 adminapi 同步软删其关系行——标签是纯管理辅助，无运行态影响）。
-- 参数：$1=deleted_at(UTC) $2=updated_at(UTC) $3=id
UPDATE {table} SET deleted_at = $1, updated_at = $2 WHERE id = $3
