-- 访问日志表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 放行请求的访问明细：与 shield_event（拦截记录）分开记录、互不关联。
-- 耗时列单位均为毫秒（ms）。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    time        TIMESTAMPTZ NOT NULL,              -- 请求完成时刻（UTC）
    trace_id    TEXT NOT NULL,                     -- 链路 ID（贯穿整条转发链）
    tenant_id   TEXT NOT NULL DEFAULT '',          -- 租户 ID（多租户预留，默认空）
    path        TEXT NOT NULL,                     -- 请求 URL 路径
    method      TEXT NOT NULL,                     -- 请求方法（GET/POST/...）
    client_ip   TEXT NOT NULL DEFAULT '',          -- 客户端 IP（已按 X-Forwarded-For 取真实地址）
    status_code INT NOT NULL,                      -- 上游返回的响应状态码
    upstream    TEXT NOT NULL DEFAULT '',          -- 实际转发的上游地址
    shield_ms   BIGINT NOT NULL DEFAULT 0,         -- 入网耗时＝请求到达→转发前（全部前置中间件）耗时（ms）；仅中间链只挂 shield 时等价防护耗时
    biz_ms      BIGINT NOT NULL DEFAULT 0,         -- 转发（业务）耗时（ms；含网关↔上游网络往返，内网部署、网络稳定时约等于业务真实处理耗时）
    total_ms    BIGINT NOT NULL DEFAULT 0,         -- 到达→出网总耗时（ms；历史行为旧口径：到转发完成）
egress_ms   BIGINT NOT NULL DEFAULT 0,         -- 出网耗时（ms）＝响应写回客户端完成 − 转发完成；历史行为 0
    req_bytes   BIGINT NOT NULL DEFAULT 0,         -- 请求体字节数
    resp_bytes  BIGINT NOT NULL DEFAULT 0,         -- 响应体字节数
    extra       TEXT NOT NULL DEFAULT '{}'         -- 扩展字段（JSON，向前兼容）
);
COMMENT ON TABLE {table} IS '访问日志表：放行请求的访问明细，与 shield_event（拦截记录）分开记录';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.time IS '请求完成时刻（UTC）';
COMMENT ON COLUMN {table}.trace_id IS '链路 ID（贯穿整条转发链）';
COMMENT ON COLUMN {table}.tenant_id IS '租户 ID（多租户预留，默认空）';
COMMENT ON COLUMN {table}.path IS '请求 URL 路径';
COMMENT ON COLUMN {table}.method IS '请求方法（GET/POST/...）';
COMMENT ON COLUMN {table}.client_ip IS '客户端 IP（已按 X-Forwarded-For 取真实地址）';
COMMENT ON COLUMN {table}.status_code IS '上游返回的响应状态码';
COMMENT ON COLUMN {table}.upstream IS '实际转发的上游地址';
COMMENT ON COLUMN {table}.shield_ms IS 'L1 防护（shield）环节耗时（ms）';
COMMENT ON COLUMN {table}.biz_ms IS '业务（上游处理）耗时（ms）';
COMMENT ON COLUMN {table}.total_ms IS '请求总耗时（ms）';
COMMENT ON COLUMN {table}.egress_ms IS '出网耗时（ms）＝响应写回客户端完成 − 转发完成；历史行为 0';
COMMENT ON COLUMN {table}.req_bytes IS '请求体字节数';
COMMENT ON COLUMN {table}.resp_bytes IS '响应体字节数';
COMMENT ON COLUMN {table}.extra IS '扩展字段（JSON，向前兼容）';
