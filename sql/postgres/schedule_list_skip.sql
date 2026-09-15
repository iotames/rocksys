-- 定时任务「该轮未执行」登记（PostgreSQL 方言）：同互斥组有任务在跑等场景，该轮到点未执行时登记 skipped。
-- 注意不更新 last_run_at——其语义为「最近执行时间（执行结束时刻）」，跳过不是执行。
-- 参数顺序：last_status(1), last_message(2), updated_at(3), name(4)
UPDATE {table}
SET last_status = $1, last_message = $2, updated_at = $3
WHERE name = $4
