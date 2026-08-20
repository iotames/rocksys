-- IP 白名单表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 动态 IP 白名单（数据库持久化，管理面录入），与 .env 配置 SHIELD_IP_WHITELIST 取并集；
-- 请求热路径只读内存快照；白名单优先于黑名单（命中直接放行短路）。
-- 索引见 ip_whitelist_create_index.sql。
--
-- ★ 软删除语义：deleted_at 非 NULL 的条目不参与匹配。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    ip          VARCHAR(45) NOT NULL COMMENT '精确 IP 或 CIDR（唯一：重复导入幂等拒绝）',
    title       VARCHAR(64) NOT NULL DEFAULT '' COMMENT '备注（如"办公出口段"）',
    deleted_at  DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除，不参与匹配',
    created_at  DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）',
    UNIQUE KEY uk_{table}_ip (ip)
) DEFAULT CHARSET=utf8mb4 COMMENT='IP 白名单表：动态 IP 白名单（管理面录入），与 .env 配置 SHIELD_IP_WHITELIST 取并集；白名单优先于黑名单（命中直接放行短路）'
