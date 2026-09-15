-- 单 IP 地理信息 upsert（MySQL 方言）：冲突更新仅刷新 geo 列与 updated_at，created_at 保持首次入库值。
-- 参数顺序：ip, country_code, country_name, province, city, created_at, updated_at
INSERT INTO {table} (ip, country_code, country_name, province, city, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    country_code = VALUES(country_code),
    country_name = VALUES(country_name),
    province     = VALUES(province),
    city         = VALUES(city),
    updated_at   = VALUES(updated_at)
