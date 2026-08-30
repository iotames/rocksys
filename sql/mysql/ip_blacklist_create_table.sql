-- IP 黑名单表（幂等建表，MySQL 方言）。{table} 为运行时表名占位符（非用户输入，安全）。
-- 动态 IP 黑名单（数据库持久化，管理面录入/批量导入），与外挂 rules/ip_blacklist.txt 取并集；
-- 请求热路径只读内存快照，本表仅管理操作/启动加载/后台刷新访问（性能红线：热路径零 DB 查询）。
-- 索引见 ip_blacklist_create_index.sql。
--
-- ★ block_type 拉黑原因类别（复用 shield_event 枚举，数值稳定，勿改动）：
--   0=其他（仅黑名单表）  1=IP黑名单  2=限流(429)  3=方法不允许  4=请求体超限(413)
--   5=风险路径  6=路径遍历  7=SQL注入  8=XSS  9=爬虫/扫描器UA  10=路径/UA规则deny
--   11=人工收录（仅黑名单表）
--   枚举定义见 plugins/shield/block_type.go（与 Go 源码保持一一对应）。
-- ★ 运行时只按 ip 精确/CIDR 匹配；block_type 仅作管理面过滤与统计（该 IP 因哪类请求被识别封禁）。
-- ★ 软删除/过期语义：deleted_at 非 NULL 或 expires_at 已过期（UTC now）的条目不参与匹配。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    ip          VARCHAR(45) NOT NULL COMMENT '精确 IP 或 CIDR（唯一：重复导入幂等拒绝）',
    title       VARCHAR(64) NOT NULL DEFAULT '' COMMENT '拉黑原因标题（如"Azure 云段扫描器"）',
    block_type  SMALLINT NOT NULL DEFAULT 1 COMMENT '拉黑原因类别 0-11：0=其他(仅黑名单表) 1=IP黑名单 2=限流 3=方法不允许 4=请求体超限 5=风险路径 6=路径遍历 7=SQL注入 8=XSS 9=爬虫/扫描器UA 10=路径/UA规则deny 11=人工收录(仅黑名单表)；仅管理面过滤统计，非运行时匹配依据',
    hit_count   INT NOT NULL DEFAULT 0 COMMENT '命中拦截计数（异步累加，观测/排序用）',
    warn_times  INT NOT NULL DEFAULT 0 COMMENT '封禁次数（人工+自动累计，限时时达 5 次转永久）',
    expires_at  DATETIME(3) NULL COMMENT '过期时间（UTC）；NULL=永久，过期条目不参与匹配',
    deleted_at  DATETIME(3) NULL COMMENT '软删除时间（UTC）；非 NULL 视为已删除，不参与匹配',
    created_at  DATETIME(3) NOT NULL COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3) NOT NULL COMMENT '最后更新时间（UTC）',
    UNIQUE KEY uk_{table}_ip (ip)
) DEFAULT CHARSET=utf8mb4 COMMENT='IP 黑名单表：动态 IP 黑名单（管理面录入/批量导入），与外挂 rules/ip_blacklist.txt 取并集；热路径只读内存快照；block_type 0-11 枚举见字段注释'
