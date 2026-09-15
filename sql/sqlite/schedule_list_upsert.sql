-- 定时任务登记 upsert（SQLite 方言）：按 name 冲突时仅刷新登记列，last_run_at/last_status/last_message
-- 运行态保持不变（登记与运行状态解耦；系统级整行重置走 schedule_list_reset.sql）。
-- 参数顺序：name, title, kind, config_key, plan, remark, updated_at
INSERT INTO {table} (name, title, kind, config_key, plan, remark, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    title      = excluded.title,
    kind       = excluded.kind,
    config_key = excluded.config_key,
    plan       = excluded.plan,
    remark     = excluded.remark,
    updated_at = excluded.updated_at
