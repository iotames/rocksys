-- Top 攻击源 IP（查询时聚合）。参数：from（起始时刻，含）、limit（返回条数）
SELECT client_ip, COUNT(*) AS cnt
FROM {table}
WHERE time >= $1 AND client_ip <> ''
GROUP BY client_ip
ORDER BY cnt DESC
LIMIT $2
