-- 后端服务器节点基础表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 登记节点资产与体检参数；登记一次、全局去重（探活去重的前提）。
-- ★ url 唯一（软删行除外）：由 create_index 复合唯一索引 (url, deleted_at) 借 NULL 不判重实现（本方言口径）。
-- ★ 探活语义：hc_path 为空 = 不主动探活，视为健康；健康状态不落库（内存单一事实源，经接口透出）。
-- 索引见 dispatch_node_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id             BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    name           VARCHAR(255) NOT NULL DEFAULT '' COMMENT '节点名称（人类可读，空允许）',
    url            VARCHAR(512) NOT NULL COMMENT '节点地址 http(s)://host[:port]（唯一：软删行除外）',
    hc_interval_ms INT NOT NULL DEFAULT 20000 COMMENT '探活周期（ms），默认 20000；hc_path 为空时不参与探活',
    hc_timeout_ms  INT NOT NULL DEFAULT 5000 COMMENT '探活超时（ms），默认 5000',
    hc_path        VARCHAR(255) NOT NULL DEFAULT '' COMMENT '探活路径（以 / 开头）；空 = 不主动探活，视为健康',
    enabled        TINYINT NOT NULL DEFAULT 1 COMMENT '启用 1/0；停用 = 不参与任何均衡器构建与探活',
    remark         VARCHAR(255) NOT NULL DEFAULT '' COMMENT '备注（人类备注）',
    deleted_at     DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建',
    created_at     DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at     DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='后端服务器节点基础表：登记节点资产与体检参数；登记一次、全局去重（探活去重的前提）；url 唯一（软删行除外）；健康状态不落库（内存单一事实源）'
