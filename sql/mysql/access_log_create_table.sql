-- 访问日志表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    time        VARCHAR(40) NOT NULL,
    trace_id    VARCHAR(255) NOT NULL,
    tenant_id   VARCHAR(255) NOT NULL DEFAULT '',
    path        VARCHAR(2048) NOT NULL,
    method      VARCHAR(16) NOT NULL,
    client_ip   VARCHAR(64) NOT NULL DEFAULT '',
    status_code INT NOT NULL,
    upstream    VARCHAR(1024) NOT NULL DEFAULT '',
    shield_ms   BIGINT NOT NULL DEFAULT 0,
    biz_ms      BIGINT NOT NULL DEFAULT 0,
    total_ms    BIGINT NOT NULL DEFAULT 0,
    req_bytes   BIGINT NOT NULL DEFAULT 0,
    resp_bytes  BIGINT NOT NULL DEFAULT 0,
    extra       TEXT NOT NULL
) DEFAULT CHARSET=utf8mb4
