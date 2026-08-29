-- 自动拉黑候选查询（IP 黑名单增强 STEP A4；设计依据 docs/IP_BLACKLIST_PLAN.md §3.4 决策 11/12）。
-- 近窗口内拦截事件按 IP × 类别聚合，返回 (client_ip, block_type, cnt)；
-- Go 侧再按 IP 跨类别合计判阈值（决策 12，不用窗口函数，三方言保持同构）。
-- ★ block_type >= 2：排除 IP 黑名单自我拦截事件（决策 11，防循环封禁）。
-- 参数：since（窗口起点，UTC；走 time 列已有 idx_time 索引）
SELECT client_ip, block_type, COUNT(*) AS cnt
FROM {table}
WHERE time >= $1 AND block_type >= 2 AND client_ip <> ''
GROUP BY client_ip, block_type
