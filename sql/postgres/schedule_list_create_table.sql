-- 定时任务只读登记表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 定位：只做登记 + 状态汇总，不驱动任何任务（GEOIP_LIST_PLAN §4.3，D18）。
-- kind 取值：configurable=可配型（关联 easyconf 开关）/ system=系统级只读（被改则重启按 name 重置）。
-- last_status 取值：success / failed / skipped / cancelled；仅回写者有意义（本期=geoip_sync）。
-- cancelled = 人工/外部取消（区别于 skipped「跳过未执行」与 failed「执行出错」）。
-- 无 enabled 列：启用状态由 GET /admin/schedule/list 行内附带（服务端读 config_key 对应配置当前值）。
CREATE TABLE IF NOT EXISTS {table} (
    id           BIGSERIAL PRIMARY KEY,
    name         VARCHAR(64) NOT NULL UNIQUE,       -- 任务唯一标识（与代码常量绑定）
    title        VARCHAR(128) NOT NULL DEFAULT '',  -- 中文名（展示）
    kind         VARCHAR(16) NOT NULL DEFAULT '',   -- configurable / system（含义见表头注释）
    config_key   VARCHAR(64) NOT NULL DEFAULT '',   -- 关联 easyconf 开关名（可配型；系统级空）
    plan         VARCHAR(64) NOT NULL DEFAULT '',   -- 计划描述（仅展示）
    last_run_at  TIMESTAMPTZ,                       -- 上次执行完成时刻（UTC；语义=执行结束）
    last_status  VARCHAR(16) NOT NULL DEFAULT '',   -- success / failed / skipped / cancelled
    last_message VARCHAR(255) NOT NULL DEFAULT '',  -- 结果摘要
    remark       VARCHAR(255) NOT NULL DEFAULT '',  -- 说明（含"不纳入原因/只读"）
    updated_at   TIMESTAMPTZ NOT NULL               -- 行最近更新时间（UTC）
);
COMMENT ON TABLE {table} IS '定时任务只读登记表：登记 + 状态汇总，不驱动任务';
COMMENT ON COLUMN {table}.id IS '自增主键';
COMMENT ON COLUMN {table}.name IS '任务唯一标识（与代码常量绑定）';
COMMENT ON COLUMN {table}.title IS '中文名（展示）';
COMMENT ON COLUMN {table}.kind IS 'configurable=可配型 system=系统级只读（被改重启重置）';
COMMENT ON COLUMN {table}.config_key IS '关联 easyconf 开关名（可配型；系统级空）';
COMMENT ON COLUMN {table}.plan IS '计划描述（仅展示）';
COMMENT ON COLUMN {table}.last_run_at IS '上次执行完成时刻（UTC；语义=执行结束）；仅回写者有意义';
COMMENT ON COLUMN {table}.last_status IS 'success / failed / skipped / cancelled';
COMMENT ON COLUMN {table}.last_message IS '结果摘要';
COMMENT ON COLUMN {table}.remark IS '说明（含"不纳入原因/只读"）';
COMMENT ON COLUMN {table}.updated_at IS '行最近更新时间（UTC）';
