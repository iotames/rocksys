-- 负载均衡器表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- ★ name 唯一（软删行除外）：由 create_index 复合唯一索引 (name, deleted_at) 借 NULL 不判重实现（本方言口径）。
-- ★ 停用（enabled=0）= 引用它的规则全部 503（WebUI 停用时提示引用数）。
-- ★ algo 均衡策略枚举（数值稳定，勿改动）：1=round_robin（平滑加权轮询，默认） 2=least_conn（最小连接优先）。
-- 索引见 dispatch_upstream_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id             BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    name           VARCHAR(255) NOT NULL COMMENT '均衡器名称（人类可读、唯一：软删行除外；如「订单服务-会话保持池」）',
    algo           TINYINT NOT NULL DEFAULT 1 COMMENT '均衡策略枚举：1=round_robin(平滑加权轮询，默认) 2=least_conn(最小连接优先)；数值稳定，勿改动',
    sticky_enabled TINYINT NOT NULL DEFAULT 0 COMMENT '会话保持 1/0，默认 0；开启后网关自种 Cookie 粘性',
    sticky_cookie  VARCHAR(128) NOT NULL DEFAULT 'rocksys_node' COMMENT 'Cookie 名，默认 rocksys_node；仅 sticky_enabled=1 时生效',
    enabled        TINYINT NOT NULL DEFAULT 1 COMMENT '启用 1/0；停用 = 引用它的规则全部 503（WebUI 停用时提示引用数）',
    remark         VARCHAR(255) NOT NULL DEFAULT '' COMMENT '备注（人类备注）',
    deleted_at     DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建',
    created_at     DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at     DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='负载均衡器表：algo 1=round_robin(默认)/2=least_conn；会话保持经网关自种 Cookie；停用 = 引用它的规则全部 503；name 唯一（软删行除外）'
