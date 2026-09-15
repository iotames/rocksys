-- IP 地理信息关联表索引（幂等，多条语句由组件拆分逐条执行）
-- 国家码索引：按国家聚合统计加速（可选优化，主键 ip 已覆盖等值点查）
CREATE INDEX IF NOT EXISTS idx_{table}_country ON {table}(country_code)
