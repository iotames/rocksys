-- 按条件查询访问日志（时间倒序专用：ORDER BY id 走主键序 + LIMIT 提前截断，免临时排序，最新在前）。
-- 缺省排序（time_desc）走本脚本；耗时排序（total/egress）走 access_log_query.sql（CASE 排序需物化后排序，低频可接受）。
-- 地理信息 LEFT JOIN geoip_list 关联返回（country_code/country_name/province/city）；
-- 未命中（IP 未同步入库）返回空串，读侧回退实时解析并做「市→省→国名→未知」兜底显示。
-- 可选条件传空串即不过滤；状态过滤已可索引化：status_lo/status_hi 区间闭区间
-- （不过滤传 0/999999；status_group '2'-'5' → 200-299 等；仅异常 → 400-999999，Go 侧合成）。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id, status_lo, status_hi, limit, offset
SELECT a.id, a.time, a.trace_id, a.tenant_id, a.path, a.method, a.client_ip, a.status_code, a.upstream,
       a.shield_ms, a.biz_ms, a.total_ms, a.egress_ms, a.req_bytes, a.resp_bytes, a.user_agent, a.extra,
       g.country_code, g.country_name, g.province, g.city
FROM {table} a
LEFT JOIN {geo} g ON g.ip = a.client_ip
WHERE a.time >= ? AND a.time <= ?
  AND (? = '' OR a.path = ?)
  AND (? = '' OR a.path LIKE '%' || ? || '%')
  AND (? = '' OR a.trace_id LIKE '%' || ? || '%')
  AND a.status_code >= ? AND a.status_code <= ?
ORDER BY a.id DESC
LIMIT ? OFFSET ?
