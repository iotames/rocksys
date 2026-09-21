-- 节点与均衡器关系表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 多对多关系（关系表带属性 weight/priority）；引用完整性由应用层（adminapi）校验，不建数据库外键。
-- ★ (upstream_id, node_id) 唯一（软删行除外）：由 create_index 复合唯一索引借 NULL 不判重实现（本方言口径）。
-- ★ 关系行整组替换语义（随均衡器表单整体保存），不设单行 update。
-- ★ priority 枚举（数值稳定，勿改动）：0=高优（默认） 1=备份（高优全挂才启用，即 NGINX backup）。
-- 索引见 dispatch_upstream_node_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    upstream_id BIGINT NOT NULL COMMENT '均衡器 → dispatch_upstream.id',
    node_id     BIGINT NOT NULL COMMENT '节点 → dispatch_node.id',
    weight      INT NOT NULL DEFAULT 1 COMMENT '权重（正整数默认 1；round_robin 平滑加权用）',
    priority    TINYINT NOT NULL DEFAULT 0 COMMENT '优先级枚举：0=高优(默认) 1=备份(高优全挂才启用，即 NGINX backup)；数值稳定，勿改动',
    deleted_at  DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建',
    created_at  DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='节点与均衡器关系表：多对多关系（带属性 weight/priority）；关系行整组替换语义，不设单行 update；(upstream_id, node_id) 唯一（软删行除外）'
