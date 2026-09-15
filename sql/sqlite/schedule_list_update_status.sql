-- 定时任务运行状态回写（SQLite 方言）：本期仅 geoip_sync 行有回写者；任务结束时单条原子更新。
-- 参数顺序：last_run_at(1), last_status(2), last_message(3), updated_at(4), name(5)
UPDATE {table}
SET last_run_at = ?, last_status = ?, last_message = ?, updated_at = ?
WHERE name = ?
