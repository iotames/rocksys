-- 流量总览汇总：一次查询输出全部标量指标（access_log 放行 + shield_event 拦截两侧）。
-- 参数顺序：from, to 依次重复 9 次（$1..$18），对应输出列顺序：
--   req_ok($1,$2) req_pv($3,$4) uv($5,$6) ip_all($7,$8) block_total($9,$10)
--   attack_ips($11,$12) err4xx($13,$14) err5xx($15,$16) block4xx($17,$18)。
-- 口径说明（UTC，边界 time >= from AND time <= to，保证与 series 各桶之和可对账）：
--   req_ok     = 放行请求总数（含静态资源、含放行后 4xx/5xx 响应）；请求次数 = req_ok + block_total。
--   req_pv     = req_ok 去静态资源。静态后缀清单硬编码（.js .css .map .ico .png .jpg .jpeg
--                .gif .svg .webp .woff .woff2 .ttf .eot）；LOWER 保证大小写不敏感；
--                path 先经 SPLIT_PART 截取 '?' 前的路径段，避免带查询串的路径漏匹配后缀。
--   uv         = COUNT(DISTINCT ROW(client_ip, COALESCE(user_agent,'')))（IP+UA 行构造去重口径，
--                ua 空串退化计 1）。
--   ip_all     = access 侧 DISTINCT client_ip。
--   block_total/attack_ips = shield_event 计数 / DISTINCT client_ip。
--   err4xx/err5xx = access 侧 status_code 400-499 / 500-599；block4xx = shield 侧 400-499。
SELECT
  (SELECT COUNT(*) FROM {table} WHERE time >= $1 AND time <= $2) AS req_ok,
  (SELECT COUNT(*) FROM {table} WHERE time >= $3 AND time <= $4
     AND NOT (LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.js'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.css'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.map'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.ico'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.png'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.jpg'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.jpeg'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.gif'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.svg'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.webp'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.woff'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.woff2'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.ttf'
       OR LOWER(SPLIT_PART(path, '?', 1)) LIKE '%.eot')) AS req_pv,
  (SELECT COUNT(DISTINCT ROW(client_ip, COALESCE(user_agent, ''))) FROM {table}
     WHERE time >= $5 AND time <= $6) AS uv,
  (SELECT COUNT(DISTINCT client_ip) FROM {table} WHERE time >= $7 AND time <= $8) AS ip_all,
  (SELECT COUNT(*) FROM {table2} WHERE time >= $9 AND time <= $10) AS block_total,
  (SELECT COUNT(DISTINCT client_ip) FROM {table2} WHERE time >= $11 AND time <= $12) AS attack_ips,
  (SELECT COUNT(*) FROM {table} WHERE time >= $13 AND time <= $14
     AND status_code >= 400 AND status_code <= 499) AS err4xx,
  (SELECT COUNT(*) FROM {table} WHERE time >= $15 AND time <= $16
     AND status_code >= 500 AND status_code <= 599) AS err5xx,
  (SELECT COUNT(*) FROM {table2} WHERE time >= $17 AND time <= $18
     AND status_code >= 400 AND status_code <= 499) AS block4xx
