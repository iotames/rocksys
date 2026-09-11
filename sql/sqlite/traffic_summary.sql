-- 流量总览汇总：一次查询输出全部标量指标（access_log 放行 + shield_event 拦截两侧）。
-- CTE 单次扫描版：access_log 范围行只扫一遍（旧版 13 个标量子查询各自全范围扫描、分位数各排一次；
-- 现收敛为 access_log 1 次扫描 + 1 次分位排序 + shield_event 1 次扫描），口径与旧版逐项一致。
-- 参数顺序：from, to, from, to（共 4 个 ?）：CTE a（access_log 范围行）与 CTE s（shield_event 聚合）各一对。
-- 输出列：req_ok req_pv uv ip_all block_total attack_ips err4xx err5xx block4xx
--         lat_avg lat_p50 lat_p95 lat_p99（读侧按列名取数）。
-- 口径说明（UTC，边界 time >= from AND time <= to，保证与 series 各桶之和可对账）：
--   req_ok     = 放行请求总数（含静态资源、含放行后 4xx/5xx 响应）；请求次数 = req_ok + block_total。
--   req_pv     = req_ok 去静态资源。静态后缀清单硬编码（.js .css .map .ico .png .jpg .jpeg
--                .gif .svg .webp .woff .woff2 .ttf .eot）；LOWER 保证大小写不敏感；
--                path 先截取 '?' 前的路径段（INSTR(path||'?','?')-1，无 '?' 时即为全路径），
--                避免带查询串的路径漏匹配后缀。范围无行时 SUM 为 NULL，COALESCE 归 0
--                （与旧版 COUNT 口径一致）。
--   uv         = COUNT(DISTINCT client_ip||'|'||COALESCE(user_agent,''))（IP+UA 口径，
--                ua 空串退化计 1）。
--   ip_all     = access 侧 DISTINCT client_ip。
--   block_total/attack_ips = shield_event 计数 / DISTINCT client_ip。
--   err4xx/err5xx = access 侧 status_code 400-499 / 500-599；block4xx = shield 侧 400-499。
--   延迟口径：total_ms 的 AVG / PERCENT_RANK 秩分位数（≤ 分位取最大值；排序集复用 CTE a，
--                一次排序同时产出三分位，METRICS_WINDOW 拆分项，随所选范围精确统计）；无行时为 NULL。
--   geo 缺失（country 空串）不影响本脚本。
WITH a AS MATERIALIZED (
  SELECT client_ip, user_agent, status_code, total_ms,
         LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) AS ppath
  FROM {table}
  WHERE time >= ? AND time <= ?
), s AS (
  SELECT COUNT(*) AS block_total,
         COUNT(DISTINCT client_ip) AS attack_ips,
         COALESCE(SUM(CASE WHEN status_code BETWEEN 400 AND 499 THEN 1 ELSE 0 END), 0) AS block4xx
  FROM {table2}
  WHERE time >= ? AND time <= ?
), r AS (
  SELECT COUNT(*) AS req_ok,
         COALESCE(SUM(CASE WHEN NOT (ppath LIKE '%.js' OR ppath LIKE '%.css' OR ppath LIKE '%.map'
                OR ppath LIKE '%.ico' OR ppath LIKE '%.png' OR ppath LIKE '%.jpg' OR ppath LIKE '%.jpeg'
                OR ppath LIKE '%.gif' OR ppath LIKE '%.svg' OR ppath LIKE '%.webp' OR ppath LIKE '%.woff'
                OR ppath LIKE '%.woff2' OR ppath LIKE '%.ttf' OR ppath LIKE '%.eot')
              THEN 1 ELSE 0 END), 0) AS req_pv,
         COUNT(DISTINCT client_ip || '|' || COALESCE(user_agent, '')) AS uv,
         COUNT(DISTINCT client_ip) AS ip_all,
         COALESCE(SUM(CASE WHEN status_code BETWEEN 400 AND 499 THEN 1 ELSE 0 END), 0) AS err4xx,
         COALESCE(SUM(CASE WHEN status_code BETWEEN 500 AND 599 THEN 1 ELSE 0 END), 0) AS err5xx,
         AVG(total_ms) AS lat_avg
  FROM a
), p AS (
  SELECT MAX(CASE WHEN pr <= 0.50 THEN total_ms END) AS lat_p50,
         MAX(CASE WHEN pr <= 0.95 THEN total_ms END) AS lat_p95,
         MAX(CASE WHEN pr <= 0.99 THEN total_ms END) AS lat_p99
  FROM (SELECT total_ms, PERCENT_RANK() OVER (ORDER BY total_ms) AS pr FROM a) d
)
SELECT r.req_ok, r.req_pv, r.uv, r.ip_all, s.block_total, s.attack_ips,
       r.err4xx, r.err5xx, s.block4xx, r.lat_avg, p.lat_p50, p.lat_p95, p.lat_p99
FROM r CROSS JOIN s CROSS JOIN p
