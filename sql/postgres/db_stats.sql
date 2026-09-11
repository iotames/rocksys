-- 数据库空间统计：当前 schema 内全部基础表的表名/备注/数据占用/索引占用（字节）。
-- 供「服务 → 数据库」页数据表概览。条数不在此查（n_live_tup 为估算值，由端点动态 COUNT(*) 精确补齐）。
-- PG 口径：pg_relation_size=表堆主体，pg_indexes_size=该表全部索引合计；
-- bytes 取 pg_total_relation_size（表 + 索引 + TOAST），故大字段表下 bytes 可能略大于
-- data_bytes+index_bytes，差额即 TOAST 溢出存储。
SELECT c.relname AS name,
       COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment,
       pg_relation_size(c.oid) AS data_bytes,
       pg_indexes_size(c.oid) AS index_bytes,
       pg_total_relation_size(c.oid) AS bytes
FROM pg_class c
WHERE c.relkind = 'r'
  AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())
ORDER BY c.relname
