-- 数据库空间统计：当前 schema 内全部基础表的表名/备注/占用空间（含索引与 TOAST，字节）。
-- 供「服务 → 数据库」页数据表概览。条数不在此查（n_live_tup 为估算值，由端点动态 COUNT(*) 精确补齐）。
SELECT c.relname AS name,
       COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment,
       pg_total_relation_size(c.oid) AS bytes
FROM pg_class c
WHERE c.relkind = 'r'
  AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())
ORDER BY c.relname
