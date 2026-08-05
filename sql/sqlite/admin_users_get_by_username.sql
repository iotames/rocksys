-- 按用户名查管理员（登录用）。未找到时无返回行。
SELECT id, username, password_hash, created_at, updated_at
FROM {table}
WHERE username = ?
