-- 路由规则标签表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 标签实体化（GitLab label 同款模型）：全局唯一、重命名一次生效、筛选下拉数据源干净。
-- chip 颜色由前端按名称哈希自动分配，不落库。
-- ★ name 全局唯一、小写（保存时归一；软删行除外）：由 create_index 复合唯一索引借 NULL 不判重实现（本方言口径）。
-- 索引见 dispatch_tag_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id         BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    name       VARCHAR(191) NOT NULL COMMENT '标签名（全局唯一、小写（保存时归一）；软删行除外）',
    deleted_at DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除；标签删除时由 adminapi 同步软删其关系行',
    created_at DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='路由规则标签表：标签实体化（GitLab label 同款模型），全局唯一、重命名一次生效；chip 颜色由前端按名称哈希自动分配，不落库'
