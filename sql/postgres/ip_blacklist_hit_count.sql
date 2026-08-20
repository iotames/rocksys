-- 黑名单命中计数异步累加（后台攒批 flush，不阻塞请求热路径）。
-- 参数：?1=累加增量 ?2=条目 id
UPDATE {table} SET hit_count = hit_count + $1 WHERE id = $2
