-- 系统级任务整行重置（MySQL 方言，D20）：被外部改动后重启按 name 重置为系统值（含运行态列清零；
-- 系统级行本就无回写者，清零即恢复权威登记值）。
-- 参数顺序：title, kind, config_key, plan, remark, updated_at, name
UPDATE {table}
SET title = ?, kind = ?, config_key = ?, plan = ?, remark = ?,
    last_run_at = NULL, last_status = '', last_message = '', updated_at = ?
WHERE name = ?
