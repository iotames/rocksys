# STEP5：shield 实时窗口桶宽化 + 落库总数 + Top IP geo 查询时解析

状态：待实施

## 目标
按 PLAN §3.4 WAF 改造：
- 内存滑动窗口 1 分钟 → 1 小时 = 60 个 1 分钟桶；`/admin/shield/metrics?window=1m|5m|15m|1h` 整分钟桶聚合返回（零误差，缺省 1m 兼容现状）
- 新增落库总数端点（查库 shield_event_count.sql 全范围，前端进页查一次）
- Top 攻击源 IP（shield_event_stats_top_ip）结果逐行 geo 查询时解析补地区字段（行数少，不加列）

## 改动文件清单
- plugins/shield/admin.go（window 参数、落库总数端点、top_ip geo 填充）
- plugins/shield 实时窗口实现文件（metrics 计数器，实施时定位）
- 相关单测（window 参数）

## 实施步骤（完成一项立即勾选保存）
- [ ] 窗口 60×1min 桶化 + window 参数聚合 ✓（go test ./plugins/shield/ -run Window）
- [ ] 落库总数端点
- [ ] top_ip geo 查询时解析注入

## 验证
- 接手核实命令：`go test ./plugins/shield/ -run 'Window|Metrics' && grep -n "window" plugins/shield/admin.go | head`
- 本步完整验证：
  - [ ] `go test ./... && go vet ./...`
  - [ ] dev 运行：?window=5m/1h 返回值与桶求和互洽；缺省 1m 与旧行为一致

## 完成标准
- 桶宽可选、缺省兼容、落库总数与 Top IP 地区可用。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 端点变更：/admin/shield/metrics 增 window 参数；新增落库总数端点
### 偏差与现场记录
- （无）
