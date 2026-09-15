-- 按条件统计访问日志总数（服务端分页 X-Total-Count 用）。
-- 状态过滤已可索引化：status_lo/status_hi 区间闭区间
-- （不过滤传 0/999999；status_group '2'-'5' → 200-299 等；仅异常 → 400-999999，Go 侧合成）。
-- 参数顺序：from, to, path, path, path_like, path_like, trace_id, trace_id, status_lo, status_hi
SELECT COUNT(*) AS cnt
FROM {table}
WHERE time >= $1 AND time <= $2
  AND ($3 = '' OR path = $4)
  AND ($5 = '' OR path LIKE '%' || $6 || '%')
  AND ($7 = '' OR trace_id LIKE '%' || $8 || '%')
  AND status_code >= $9 AND status_code <= $10
