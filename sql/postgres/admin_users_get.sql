-- 返回唯一管理员（超管只有一个）。无记录时无返回行。
SELECT id, username, password_hash, created_at, updated_at
FROM {table}
ORDER BY id ASC
LIMIT 1
