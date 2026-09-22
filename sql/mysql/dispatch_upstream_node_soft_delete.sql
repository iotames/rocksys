-- 软删除节点与均衡器关系（deleted_at = now；节点存在未软删关系引用时由 adminapi 拒绝删除节点）。
-- 关系行整组替换语义：常规编辑走「软删旧行 + 插入新行」，不设单行 update/restore。
-- 参数：?1=deleted_at(UTC) ?2=updated_at(UTC) ?3=id
UPDATE {table} SET deleted_at = ?, updated_at = ? WHERE id = ?
