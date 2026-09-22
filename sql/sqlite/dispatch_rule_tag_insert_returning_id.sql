-- 插入一条规则与标签关系。
-- 说明：本方言支持 Result.LastInsertId，执行本脚本后由 Go 侧经 Result.LastInsertId 取回自增 id。
-- 参数：?1=rule_id ?2=tag_id ?3=created_at(UTC) ?4=updated_at(UTC)
INSERT INTO {table} (rule_id, tag_id, created_at, updated_at)
VALUES (?, ?, ?, ?)
