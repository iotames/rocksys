-- mq 异步消息 outbox 表（幂等建表，SQLite 方言）。
-- {table} 为运行时表名占位符，由组件构造参数替换（非用户输入，安全）。
-- 语义：本地先落库（outbox 模式），后台轮询投递到消费方，投递成功后标记 done。
-- status 取值：pending=待投递  failed=投递失败（可重试）  done=已投递成功  dead=超过重试上限转死信。
CREATE TABLE IF NOT EXISTS {table} (
    id          INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    topic       TEXT NOT NULL,                     -- 消息主题（路由到消费方）
    payload     TEXT NOT NULL,                     -- 消息体（JSON）
    status      TEXT NOT NULL DEFAULT 'pending',   -- 状态：pending/failed/done/dead（含义见表头注释）
    retry_count INTEGER NOT NULL DEFAULT 0,        -- 已重试次数（超过上限转 dead）
    last_error  TEXT NOT NULL DEFAULT '',          -- 最近一次投递失败的错误信息
    created_at  DATETIME NOT NULL                  -- 创建时间（UTC）
)
