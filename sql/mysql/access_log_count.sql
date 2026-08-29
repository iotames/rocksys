-- 按条件统计访问日志总数（服务端分页 X-Total-Count 用）；status_group 传状态码首字符（'2'-'5'）。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id, status_group, status_group, only_error
SELECT COUNT(*) AS cnt
FROM {table}
WHERE time >= ? AND time <= ?
  AND (? = '' OR path = ?)
  AND (? = '' OR path LIKE CONCAT('%', ?, '%'))
  AND (? = '' OR trace_id LIKE CONCAT('%', ?, '%'))
  AND (? = '' OR SUBSTR(CAST(status_code AS CHAR), 1, 1) = ?)
  AND (? = 0 OR status_code >= 400)
