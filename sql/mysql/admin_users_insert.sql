-- 插入管理员。参数顺序：username, password_hash, created_at, updated_at
INSERT INTO {table} (username, password_hash, created_at, updated_at)
VALUES (?, ?, ?, ?)
