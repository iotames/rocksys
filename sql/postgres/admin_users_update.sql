-- 更新管理员（重置改用户名/密码）。参数顺序：username, password_hash, updated_at, id
UPDATE {table}
SET username = $1, password_hash = $2, updated_at = $3
WHERE id = $4
