-- 按条件统计访问日志总数（服务端分页 X-Total-Count 用）；status_group 传状态码首字符（'2'-'5'）。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id, status_group, status_group, only_error
SELECT COUNT(*) AS cnt
FROM {table}
WHERE time >= $1 AND time <= $2
  AND ($3 = '' OR path = $4)
  AND ($5 = '' OR path LIKE '%' || $6 || '%')
  AND ($7 = '' OR trace_id LIKE '%' || $8 || '%')
  AND ($9 = '' OR SUBSTR(CAST(status_code AS TEXT), 1, 1) = $10)
  AND ($11 = 0 OR status_code >= 400)
