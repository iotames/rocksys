-- 插入一条负载均衡器。
-- 说明：本方言支持 Result.LastInsertId，执行本脚本后由 Go 侧经 Result.LastInsertId 取回自增 id
--（同一连接内 LAST_INSERT_ID() 语义，无需 SELECT LAST_INSERT_ID() 追加查询）。
-- 参数：?1=name ?2=algo ?3=sticky_enabled ?4=sticky_cookie ?5=enabled ?6=remark ?7=created_at(UTC) ?8=updated_at(UTC)
INSERT INTO {table} (name, algo, sticky_enabled, sticky_cookie, enabled, remark, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
