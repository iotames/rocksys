-- 定时任务只读登记表（幂等建表，SQLite 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 定位：只做登记 + 状态汇总，不驱动任何任务（GEOIP_LIST_PLAN §4.3，D18）。
-- kind 取值：configurable=可配型（关联 easyconf 开关）/ system=系统级只读（被改则重启按 name 重置）。
-- last_status 取值：success / failed / skipped / cancelled；仅回写者有意义（本期=geoip_sync）。
-- cancelled = 人工/外部取消（区别于 skipped「跳过未执行」与 failed「执行出错」）。
-- 无 enabled 列：启用状态由 GET /admin/schedule/list 行内附带（服务端读 config_key 对应配置当前值）。
CREATE TABLE IF NOT EXISTS {table} (
    id           INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    name         TEXT NOT NULL UNIQUE,              -- 任务唯一标识（与代码常量绑定）
    title        TEXT NOT NULL DEFAULT '',          -- 中文名（展示）
    kind         TEXT NOT NULL DEFAULT '',          -- configurable / system（含义见表头注释）
    config_key   TEXT NOT NULL DEFAULT '',          -- 关联 easyconf 开关名（可配型；系统级空）
    plan         TEXT NOT NULL DEFAULT '',          -- 计划描述（仅展示：every@1h / every@24h / window/3）
    last_run_at  DATETIME,                          -- 上次执行完成时刻（UTC；语义=执行结束）；仅回写者有意义
    last_status  TEXT NOT NULL DEFAULT '',          -- success / failed / skipped / cancelled（含义见表头注释）
    last_message TEXT NOT NULL DEFAULT '',          -- 结果摘要
    remark       TEXT NOT NULL DEFAULT '',          -- 说明（含"不纳入原因/只读"）
    updated_at   DATETIME NOT NULL                  -- 行最近更新时间（UTC）
)
