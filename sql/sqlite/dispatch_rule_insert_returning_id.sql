-- 插入一条路由规则。
-- 说明：本方言支持 Result.LastInsertId，执行本脚本后由 Go 侧经 Result.LastInsertId 取回自增 id。
-- 参数：?1=match_order ?2=domain ?3=path_type ?4=path_value ?5=title ?6=upstream_id ?7=enabled ?8=remark ?9=created_at(UTC) ?10=updated_at(UTC)
INSERT INTO {table} (match_order, domain, path_type, path_value, title, upstream_id, enabled, remark, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
