-- 插入一条后端服务器节点。
-- 说明：本方言支持 Result.LastInsertId，执行本脚本后由 Go 侧经 Result.LastInsertId 取回自增 id
--（同一连接内 LAST_INSERT_ID() 语义，无需 SELECT LAST_INSERT_ID() 追加查询）。
-- 参数：?1=name ?2=url ?3=hc_interval_ms ?4=hc_timeout_ms ?5=hc_path ?6=enabled ?7=remark ?8=created_at(UTC) ?9=updated_at(UTC)
INSERT INTO {table} (name, url, hc_interval_ms, hc_timeout_ms, hc_path, enabled, remark, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
