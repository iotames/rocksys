-- SQL 执行审计表索引（幂等，多条语句由组件拆分逐条执行）
CREATE INDEX IF NOT EXISTS idx_sql_exec_log_time ON {table}(time)
CREATE INDEX IF NOT EXISTS idx_sql_exec_log_batch ON {table}(batch_id)
