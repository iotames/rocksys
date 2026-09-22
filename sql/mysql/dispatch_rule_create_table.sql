-- 路由规则表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- ★ 匹配语义：match_order 1–999 升序、命中即停；域名默认兜底规则建议 999。
-- ★ domain 空 = 匹配任意域名；非空 = 精确匹配、剥端口、转小写（无通配）；
--   保存时归一落库（防外部改库存入未归一值永不命中），Rebuild 构建期归一进快照。
-- ★ path_type 枚举（数值稳定，勿改动）：1=前缀（段对齐） 2=精确 3=模式（:param/*）。
--   path_value 以 / 开头，按 path_type 解释；/ + 前缀 = 全路径兜底。
-- ★ 标签不在本表存列，经 dispatch_tag / dispatch_rule_tag 两表关联。
-- 索引见 dispatch_rule_create_index.sql。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    match_order INT NOT NULL COMMENT '匹配序号（1-999 升序、命中即停；域名默认兜底建议 999）',
    domain      VARCHAR(255) NOT NULL DEFAULT '' COMMENT '域名（可选）；空=匹配任意域名；非空=精确匹配、剥端口、转小写（无通配，保存时归一落库）',
    path_type   TINYINT NOT NULL DEFAULT 1 COMMENT '路径类型枚举：1=前缀(段对齐) 2=精确 3=模式(:param/*)；数值稳定，勿改动',
    path_value  VARCHAR(512) NOT NULL COMMENT '路径值（以 / 开头，按 path_type 解释；/ + 前缀 = 全路径兜底）',
    title       VARCHAR(255) NOT NULL DEFAULT '' COMMENT '规则标题（人类可读，空允许）',
    upstream_id BIGINT NOT NULL COMMENT '均衡器 → dispatch_upstream.id（命中规则的转发目标）',
    enabled     TINYINT NOT NULL DEFAULT 1 COMMENT '启用 1/0；停用行不参与匹配，保留配置',
    remark      VARCHAR(255) NOT NULL DEFAULT '' COMMENT '备注（人类备注）',
    deleted_at  DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除，不参与任何运行期构建',
    created_at  DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）'
) DEFAULT CHARSET=utf8mb4 COMMENT='路由规则表：match_order 升序命中即停；domain 精确匹配（保存时归一落库）；path_type 1=前缀/2=精确/3=模式；标签经 dispatch_tag/dispatch_rule_tag 关联'
