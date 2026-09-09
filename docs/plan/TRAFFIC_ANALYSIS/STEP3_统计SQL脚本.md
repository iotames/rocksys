# STEP3：traffic 统计 SQL 脚本（三方言）+ 单测

状态：实施中

## 目标
按 PLAN §3.4 新增三方言各 4 个脚本（照 stats_daily 日期范式与 UTC 口径；时间边界 `time >= from AND time <= to`）：
- traffic_summary.sql：标量子查询一次出 req_ok/req_pv/uv/ip_all/block_total/attack_ips/err4xx/err5xx/block4xx；PV 后缀清单硬编码于 SQL 过滤条件（.js .css .map .ico .png .jpg .jpeg .gif .svg .webp .woff .woff2 .ttf .eot，注释说明）
- traffic_series_hour.sql / traffic_series_day.sql：两表 UNION ALL（来源标记）按桶 GROUP BY 条件聚合 bucket/ok_count/blocked_count
- traffic_geo_top.sql：参数 from/to/source，country GROUP BY 计数倒序
- UV 口径三方言：sqlite `COUNT(DISTINCT client_ip||'|'||COALESCE(user_agent,''))`、MySQL `COUNT(DISTINCT client_ip, COALESCE(user_agent,''))`、PG `COUNT(DISTINCT ROW(client_ip, COALESCE(user_agent,'')))`
- geo 分组空串计「未知」参与排序（SQL 侧输出原始空串、前端/读侧映射显示，或 SQL 直接 CASE——实施时定并记录）

## 改动文件清单
- sql/{sqlite,mysql,postgres}/traffic_summary.sql、traffic_series_hour.sql、traffic_series_day.sql、traffic_geo_top.sql（新增，带中文头注释）
- plugins/obs/traffic_sql_test.go 或 internal/db 下 sqlite 内存库单测（含 UV 断言：同 IP 不同 UA 计 2、ua 空串退化计 1）
- bin/hotscripts/sql 同步

## 实施步骤（完成一项立即勾选保存）
- [ ] sqlite 三方 4 脚本 ✓（单测通过）
- [ ] mysql / postgres 4 脚本
- [ ] sqlite 内存库单测（UV 断言、桶求和=总数对账）✓（go test，通过）
- [ ] 真库门控集成验证（MYSQL_TEST_DSN/PG_TEST_DSN，PLAN §5.1）

## 验证
- 接手核实命令：`ls sql/*/traffic_*.sql | wc -l`（=12）`&& go test ./plugins/obs/ -run Traffic`
- 本步完整验证：
  - [ ] `go test ./...`
  - [ ] 真库门控跑通语法与聚合

## 完成标准
- 12 个脚本齐备；UV 断言与对账断言通过；三方言真库语法通过。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 新增脚本：sql/<dbtype>/traffic_summary.sql、traffic_series_hour.sql、traffic_series_day.sql、traffic_geo_top.sql
- 新增测试：<实施时定名>
### 偏差与现场记录
- （无）
