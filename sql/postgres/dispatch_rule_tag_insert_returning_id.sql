-- 插入一条规则与标签关系并返回新行自增 id（RETURNING）。
-- PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，对本方言专用此脚本取回 id。
-- 参数：$1=rule_id $2=tag_id $3=created_at(UTC) $4=updated_at(UTC)
INSERT INTO {table} (rule_id, tag_id, created_at, updated_at)
VALUES ($1, $2, $3, $4)
RETURNING id
