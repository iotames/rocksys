-- 系统级任务整行重置 upsert（SQLite 方言，D20）：装配期登记系统级行；被外部改动后重启按 name
-- 整行覆写为系统值（含运行态列清零——系统级行本就无回写者，清零即恢复权威登记值）。
-- 参数顺序：name, title, kind, config_key, plan, remark, updated_at
INSERT INTO {table} (name, title, kind, config_key, plan, remark, last_run_at, last_status, last_message, updated_at)
VALUES (?, ?, ?, ?, ?, ?, NULL, '', '', ?)
ON CONFLICT(name) DO UPDATE SET
    title        = excluded.title,
    kind         = excluded.kind,
    config_key   = excluded.config_key,
    plan         = excluded.plan,
    remark       = excluded.remark,
    last_run_at  = NULL,
    last_status  = '',
    last_message = '',
    updated_at   = excluded.updated_at
