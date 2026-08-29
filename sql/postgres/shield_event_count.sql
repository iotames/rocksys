-- 按条件统计 WAF 拦截明细总数（服务端分页 X-Total-Count 用）。
-- 可选条件：block_type=0 表示不过滤；client_ip 空串表示不过滤。
-- 参数顺序：from, to, block_type, block_type, client_ip, client_ip
SELECT COUNT(*) AS cnt
FROM {table}
WHERE time >= $1 AND time <= $2
  AND ($3 = 0 OR block_type = $4)
  AND ($5 = '' OR client_ip = $6)
