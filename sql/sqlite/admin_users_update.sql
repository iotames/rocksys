-- 更新管理员（重置改用户名/密码）。参数顺序：username, password_hash, updated_at, id
UPDATE {table}
SET username = ?, password_hash = ?, updated_at = ?
WHERE id = ?
