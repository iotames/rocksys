-- 单 IP 地理信息 upsert（PostgreSQL 方言）：冲突更新仅刷新 geo 列与 updated_at，created_at 保持首次入库值。
-- 参数顺序：ip, country_code, country_name, province, city, created_at, updated_at
INSERT INTO {table} (ip, country_code, country_name, province, city, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT(ip) DO UPDATE SET
    country_code = excluded.country_code,
    country_name = excluded.country_name,
    province     = excluded.province,
    city         = excluded.city,
    updated_at   = excluded.updated_at
