-- 访问日志表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 放行请求的访问明细：与 shield_event（拦截记录）分开记录、互不关联。
-- 耗时列单位均为毫秒（ms）。索引见 access_log_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    time        DATETIME(3) NOT NULL COMMENT '请求完成时刻（UTC）',
    trace_id    VARCHAR(64) NOT NULL COMMENT '链路 ID（贯穿整条转发链）',
    tenant_id   VARCHAR(64) NOT NULL DEFAULT '' COMMENT '租户 ID（多租户预留，默认空）',
    path        VARCHAR(2048) NOT NULL COMMENT '请求 URL 路径',
    method      VARCHAR(16) NOT NULL COMMENT '请求方法（GET/POST/...）',
    client_ip   VARCHAR(64) NOT NULL DEFAULT '' COMMENT '客户端 IP（已按 X-Forwarded-For 取真实地址）',
    status_code INT NOT NULL COMMENT '上游返回的响应状态码',
    upstream    VARCHAR(255) NOT NULL DEFAULT '' COMMENT '实际转发的上游地址',
    shield_ms   BIGINT NOT NULL DEFAULT 0 COMMENT '入网耗时＝请求到达→转发前（全部前置中间件）耗时（ms）；仅中间链只挂 shield 时等价防护耗时',
    biz_ms      BIGINT NOT NULL DEFAULT 0 COMMENT '转发（业务）耗时（ms；含网关↔上游网络往返，内网部署、网络稳定时约等于业务真实处理耗时）',
    total_ms    BIGINT NOT NULL DEFAULT 0 COMMENT '到达→出网总耗时（ms；历史行为旧口径：到转发完成）',
    egress_ms   BIGINT NOT NULL DEFAULT 0 COMMENT '出网耗时（ms）＝响应写回客户端完成 − 转发完成；历史行为 0',
    req_bytes   BIGINT NOT NULL DEFAULT 0 COMMENT '请求体字节数',
    resp_bytes  BIGINT NOT NULL DEFAULT 0 COMMENT '响应体字节数',
    extra       TEXT NOT NULL COMMENT '扩展字段（JSON，向前兼容）'
) DEFAULT CHARSET=utf8mb4 COMMENT='访问日志表：放行请求的访问明细，与 shield_event（拦截记录）分开记录'
