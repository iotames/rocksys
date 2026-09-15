-- 按条件查询 WAF 拦截明细（id 倒序，最新在前），支持 offset 服务端分页。
-- 地理信息 LEFT JOIN geoip_list 关联返回（country_code/country_name/province/city）；
-- 未命中（IP 未同步入库）返回空串，读侧回退实时解析并做「市→省→国名→未知」兜底显示。
-- 可选条件：block_type=0 表示不过滤；client_ip 空串表示不过滤。
-- 参数顺序：from, to, block_type, block_type, client_ip, client_ip, limit, offset
SELECT a.id, a.time, a.trace_id, a.block_type, a.client_ip, a.method, a.path, a.raw_url, a.user_agent, a.host, a.status_code, a.rule_hit, a.req_bytes, a.extra,
       g.country_code, g.country_name, g.province, g.city
FROM {table} a
LEFT JOIN {geo} g ON g.ip = a.client_ip
WHERE a.time >= ? AND a.time <= ?
  AND (? = 0 OR a.block_type = ?)
  AND (? = '' OR a.client_ip = ?)
ORDER BY a.id DESC
LIMIT ? OFFSET ?
