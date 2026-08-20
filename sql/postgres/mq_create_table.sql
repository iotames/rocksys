-- mq 异步消息 outbox 表（幂等建表，PostgreSQL 方言）。
-- {table} 为运行时表名占位符，由组件构造参数替换（非用户输入，安全）。
-- 语义：本地先落库（outbox 模式），后台轮询投递到消费方，投递成功后标记 done。
-- status 取值：pending=待投递  failed=投递失败（可重试）  done=已投递成功  dead=超过重试上限转死信。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    topic       TEXT NOT NULL,
    payload     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    retry_count INT NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL
);
COMMENT ON TABLE {table} IS 'mq 异步消息 outbox 表：本地先落库、后台轮询投递（outbox 模式）';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.topic IS '消息主题（路由到消费方）';
COMMENT ON COLUMN {table}.payload IS '消息体（JSON）';
COMMENT ON COLUMN {table}.status IS '状态：pending=待投递 failed=投递失败(可重试) done=已投递成功 dead=超重试上限转死信';
COMMENT ON COLUMN {table}.retry_count IS '已重试次数（超过上限转 dead）';
COMMENT ON COLUMN {table}.last_error IS '最近一次投递失败的错误信息';
COMMENT ON COLUMN {table}.created_at IS '创建时间（UTC）';
