-- IP 地理信息关联表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 一 IP 一行：地理信息是 IP 的函数，独立成表与日志表按 client_ip 关联，
-- 取代 access_log / shield_event 逐行冗余存 country/city。
-- 名实相符：country_code 存 ISO 码（聚合口径）、country_name 存本地化国名（显示，zh-CN 优先）、
-- province 存一级行政区全称（zh-CN，如「广东省」；中国地图着色依赖全称，禁止存短名）、city 只存城市名。
-- 数据不造假：解析不出的字段保持空串，拼接/兜底只在读侧显示。
CREATE TABLE IF NOT EXISTS {table} (
    ip           TEXT PRIMARY KEY,          -- 纯 IP 文本（与日志表 client_ip 同格式，无端口）
    country_code TEXT NOT NULL DEFAULT '',  -- ISO alpha-2 国家码（聚合口径 + 世界地图着色）
    country_name TEXT NOT NULL DEFAULT '',  -- 本地化国名 zh-CN 优先（显示）
    province     TEXT NOT NULL DEFAULT '',  -- 一级行政区（省/州；zh-CN 全称；中国地图着色）
    city         TEXT NOT NULL DEFAULT '',  -- 城市名（仅市）
    created_at   DATETIME NOT NULL,         -- 首次解析入库时间（UTC）
    updated_at   DATETIME NOT NULL          -- 该 IP 最近解析时间（UTC）；冲突更新时保持 created_at 不变
)
