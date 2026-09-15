-- 定时任务登记清单（sqlite 方言，GET /admin/schedule/list 数据源）：只读全量输出，enabled 由服务端按配置现值计算。
SELECT name, title, kind, config_key, plan, last_run_at, last_status, last_message, remark
FROM {table}
ORDER BY kind, name
