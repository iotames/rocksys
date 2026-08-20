-- mq 异步消息 outbox 表（幂等建表，MySQL 方言）。
-- {table} 为运行时表名占位符，由组件构造参数替换（非用户输入，安全）。
-- 语义：本地先落库（outbox 模式），后台轮询投递到消费方，投递成功后标记 done。
-- status 取值：pending=待投递  failed=投递失败（可重试）  done=已投递成功  dead=超过重试上限转死信。
-- 索引见 mq_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    topic       VARCHAR(255) NOT NULL COMMENT '消息主题（路由到消费方）',
    payload     TEXT NOT NULL COMMENT '消息体（JSON）',
    status      VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending=待投递 failed=投递失败(可重试) done=已投递成功 dead=超重试上限转死信',
    retry_count INT NOT NULL DEFAULT 0 COMMENT '已重试次数（超过上限转 dead）',
    last_error  VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '最近一次投递失败的错误信息（VARCHAR 而非 TEXT：MySQL 8.0.13 以下 TEXT 列不支持 DEFAULT）',
    created_at  DATETIME(3) NOT NULL COMMENT '创建时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='mq 异步消息 outbox 表：本地先落库、后台轮询投递（outbox 模式）'
