-- 流量总览汇总：一次查询输出全部标量指标（access_log 放行 + shield_event 拦截两侧）。
-- 参数顺序：from, to 依次重复 9 次（共 18 个 ?），对应输出列顺序：
--   req_ok(1,2) req_pv(3,4) uv(5,6) ip_all(7,8) block_total(9,10)
--   attack_ips(11,12) err4xx(13,14) err5xx(15,16) block4xx(17,18) lat_avg(19,20)
--   lat_p50(21,22) lat_p95(23,24) lat_p99(25,26)（PERCENT_RANK 窗口函数，≤ 分位取最大值）。
--   延迟口径：total_ms 的 AVG / 最近秩分位数（METRICS_WINDOW 拆分项，随所选范围精确统计）；无行时为 NULL。
-- 口径说明（UTC，边界 time >= from AND time <= to，保证与 series 各桶之和可对账）：
--   req_ok     = 放行请求总数（含静态资源、含放行后 4xx/5xx 响应）；请求次数 = req_ok + block_total。
--   req_pv     = req_ok 去静态资源。静态后缀清单硬编码（.js .css .map .ico .png .jpg .jpeg
--                .gif .svg .webp .woff .woff2 .ttf .eot）；LOWER 保证大小写不敏感；
--                path 先截取 '?' 前的路径段（INSTR(path||'?','?')-1，无 '?' 时即为全路径），
--                避免带查询串的路径漏匹配后缀。
--   uv         = COUNT(DISTINCT client_ip||'|'||COALESCE(user_agent,''))（IP+UA 口径，
--                ua 空串退化计 1）。
--   ip_all     = access 侧 DISTINCT client_ip。
--   block_total/attack_ips = shield_event 计数 / DISTINCT client_ip。
--   err4xx/err5xx = access 侧 status_code 400-499 / 500-599；block4xx = shield 侧 400-499。
--   geo 缺失（country 空串）不影响本脚本。
SELECT
  (SELECT COUNT(*) FROM {table} WHERE time >= ? AND time <= ?) AS req_ok,
  (SELECT COUNT(*) FROM {table} WHERE time >= ? AND time <= ?
     AND NOT (LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.js'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.css'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.map'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.ico'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.png'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.jpg'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.jpeg'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.gif'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.svg'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.webp'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.woff'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.woff2'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.ttf'
       OR LOWER(SUBSTR(path, 1, INSTR(path || '?', '?') - 1)) LIKE '%.eot')) AS req_pv,
  (SELECT COUNT(DISTINCT client_ip || '|' || COALESCE(user_agent, '')) FROM {table}
     WHERE time >= ? AND time <= ?) AS uv,
  (SELECT COUNT(DISTINCT client_ip) FROM {table} WHERE time >= ? AND time <= ?) AS ip_all,
  (SELECT COUNT(*) FROM {table2} WHERE time >= ? AND time <= ?) AS block_total,
  (SELECT COUNT(DISTINCT client_ip) FROM {table2} WHERE time >= ? AND time <= ?) AS attack_ips,
  (SELECT COUNT(*) FROM {table} WHERE time >= ? AND time <= ?
     AND status_code >= 400 AND status_code <= 499) AS err4xx,
  (SELECT COUNT(*) FROM {table} WHERE time >= ? AND time <= ?
     AND status_code >= 500 AND status_code <= 599) AS err5xx,
  (SELECT COUNT(*) FROM {table2} WHERE time >= ? AND time <= ?
     AND status_code >= 400 AND status_code <= 499) AS block4xx,
  (SELECT AVG(total_ms) FROM {table} WHERE time >= ? AND time <= ?) AS lat_avg,
  (SELECT MAX(total_ms) FROM
     (SELECT total_ms, PERCENT_RANK() OVER (ORDER BY total_ms) AS pr
      FROM {table} WHERE time >= ? AND time <= ?) d WHERE pr <= 0.50) AS lat_p50,
  (SELECT MAX(total_ms) FROM
     (SELECT total_ms, PERCENT_RANK() OVER (ORDER BY total_ms) AS pr
      FROM {table} WHERE time >= ? AND time <= ?) d WHERE pr <= 0.95) AS lat_p95,
  (SELECT MAX(total_ms) FROM
     (SELECT total_ms, PERCENT_RANK() OVER (ORDER BY total_ms) AS pr
      FROM {table} WHERE time >= ? AND time <= ?) d WHERE pr <= 0.99) AS lat_p99
