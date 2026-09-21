-- 软删除规则与标签关系（deleted_at = now；关系行整组替换语义，不设单行 update/restore）。
-- 标签删除时由 adminapi 同步软删其全部关系行。
-- 参数：$1=deleted_at(UTC) $2=updated_at(UTC) $3=id
UPDATE {table} SET deleted_at = $1, updated_at = $2 WHERE id = $3
