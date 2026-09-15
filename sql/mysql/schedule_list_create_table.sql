-- 定时任务只读登记表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 定位：只做登记 + 状态汇总，不驱动任何任务（GEOIP_LIST_PLAN §4.3，D18）。
-- kind 取值：configurable=可配型（关联 easyconf 开关）/ system=系统级只读（被改则重启按 name 重置）。
-- last_status 取值：success / failed / skipped；仅回写者有意义（本期=geoip_sync）。
-- 无 enabled 列：启用状态由 GET /admin/schedule/list 行内附带（服务端读 config_key 对应配置当前值）。
CREATE TABLE IF NOT EXISTS {table} (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    name         VARCHAR(64) NOT NULL COMMENT '任务唯一标识（与代码常量绑定）',
    title        VARCHAR(128) NOT NULL DEFAULT '' COMMENT '中文名（展示）',
    kind         VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'configurable=可配型 system=系统级只读（被改重启重置）',
    config_key   VARCHAR(64) NOT NULL DEFAULT '' COMMENT '关联 easyconf 开关名（可配型；系统级空）',
    plan         VARCHAR(64) NOT NULL DEFAULT '' COMMENT '计划描述（仅展示）',
    last_run_at  DATETIME(3) NULL COMMENT '上次执行完成时刻（UTC；语义=执行结束）；仅回写者有意义',
    last_status  VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'success / failed / skipped',
    last_message VARCHAR(255) NOT NULL DEFAULT '' COMMENT '结果摘要',
    remark       VARCHAR(255) NOT NULL DEFAULT '' COMMENT '说明（含"不纳入原因/只读"）',
    updated_at   DATETIME(3) NOT NULL COMMENT '行最近更新时间（UTC）',
    UNIQUE KEY uk_{table}_name (name)
) DEFAULT CHARSET=utf8mb4 COMMENT='定时任务只读登记表：登记 + 状态汇总，不驱动任务'
