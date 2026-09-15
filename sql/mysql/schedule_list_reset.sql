-- 系统级任务整行重置 upsert（MySQL 方言，D20）：装配期登记系统级行；被外部改动后重启按 name
-- 整行覆写为系统值（含运行态列清零——系统级行本就无回写者，清零即恢复权威登记值）。
-- 参数顺序：name, title, kind, config_key, plan, remark, updated_at
INSERT INTO {table} (name, title, kind, config_key, plan, remark, last_run_at, last_status, last_message, updated_at)
VALUES (?, ?, ?, ?, ?, ?, NULL, '', '', ?)
ON DUPLICATE KEY UPDATE
    title        = VALUES(title),
    kind         = VALUES(kind),
    config_key   = VALUES(config_key),
    plan         = VALUES(plan),
    remark       = VALUES(remark),
    last_run_at  = NULL,
    last_status  = '',
    last_message = '',
    updated_at   = VALUES(updated_at)
