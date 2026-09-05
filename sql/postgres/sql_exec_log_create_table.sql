-- SQL 执行审计表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 记录管理端「数据库同步 / 执行SQL」的每条语句执行留痕：每条语句一行，遇错即停（后续未执行的语句不落表）。
CREATE TABLE IF NOT EXISTS {table} (
    id            BIGSERIAL PRIMARY KEY,             -- 自增主键
    time          TIMESTAMPTZ NOT NULL,              -- 语句执行完成时刻（UTC）
    batch_id      VARCHAR(32) NOT NULL,              -- 批次标识：同一次「执行SQL」提交归一组（随机 hex）
    seq           INTEGER NOT NULL,                  -- 批内序号（从 1 起，按拆句顺序）
    sql_text      TEXT NOT NULL,                     -- 执行的 SQL 语句原文（完整保留，单条）
    ok            SMALLINT NOT NULL DEFAULT 0,       -- 执行结果：1 成功 / 0 失败
    rows_affected INTEGER NOT NULL DEFAULT 0,        -- 受影响行数（DDL 通常为 0）
    error         TEXT NOT NULL DEFAULT '',          -- 失败原因（成功为空串）
    duration_ms   INTEGER NOT NULL DEFAULT 0,        -- 单条语句执行耗时（ms）
    client_ip     VARCHAR(45) NOT NULL DEFAULT '',   -- 发起执行的客户端 IP（审计归属）
    source        VARCHAR(32) NOT NULL DEFAULT 'webui' -- 触发来源（预留扩展：webui/api/…）
)
