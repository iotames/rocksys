-- 插入一条路由规则标签。
-- 说明：本方言支持 Result.LastInsertId，执行本脚本后由 Go 侧经 Result.LastInsertId 取回自增 id。
-- 参数：?1=name ?2=created_at(UTC) ?3=updated_at(UTC)
INSERT INTO {table} (name, created_at, updated_at)
VALUES (?, ?, ?)
