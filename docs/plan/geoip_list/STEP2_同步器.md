# STEP2：GeoIP 同步器——geoSyncAll 增量构建 geoip_list + 定时器

状态：已实施

## 目标
geoip 包 GeoInfo 拆 Province（province 全称 + city 仅市）；geoip_sync.go 发现/回填两段重写为「client_ip 未入 geoip_list」增量构建（沿用 id 游标分块 + 20s 预算 + 互斥 + 5000 IP 上限）；注册 GEOIP_SYNC_INTERVAL（int 分钟，默认 60、0=关闭、最小 10、mmdb 未加载不启动定时器、改值下轮生效）；同步成功后清 obs 流量统计缓存（D25）。

## 改动文件清单
- internal/geoip/geoip.go（+Province；joinNames 拆分）
- cmd/rocksys/geoip_sync.go（重写发现/回填段为 upsert geoip_list；报告字段适配）
- cmd/rocksys/geoip_sync_test.go（测试适配 + 新增）
- cmd/rocksys/main.go（注册 GEOIP_SYNC_INTERVAL + 定时器 goroutine + geoSyncAll 后清缓存）

## 实施步骤
- [x] geoip.go：GeoInfo 增加 Province 字段；解析拆省/市 ✓（go test ./internal/geoip/，通过）
- [x] geoip_list_upsert 接入 ✓（TestGeoipSyncUpsertConflict 通过）
- [x] 发现阶段：id 游标分块 + IN 点查求差 ✓（TestGeoipSyncCursorResume 通过）
- [x] GEOIP_SYNC_INTERVAL 注册 + 定时器 ✓（TestNormalizeGeoSyncInterval + startGeoSyncTimer 前置判定，通过）
- [x] 同步成功后清 traffic 缓存 ✓（Obs.PurgeTrafficCache；手动端点与定时器两触发点均已接线）
- [x] 测试更新全绿 ✓（go test ./cmd/rocksys/ ./internal/geoip/ ./plugins/obs/ -count=1 全过）

## 验证
- 接手核实命令：`go test ./cmd/rocksys/ ./internal/geoip/ -count=1`
- 本步完整验证：
  - [x] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys` ✓
  - [x] `go test ./cmd/rocksys/ ./internal/geoip/ ./plugins/obs/ -count=1` ✓

## 完成标准
- 同步器对内存 sqlite 库可构建 geoip_list（私网/解析不出 IP 跳过并计数）；游标/中断语义测试通过；定时器受间隔与 mmdb 前置约束（以代码审读+单测覆盖配置解析纯函数）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增配置项：GEOIP_SYNC_INTERVAL（int 分钟，包级变量 geoipSyncIntervalMin 由 Register 绑定热更）
- 新增/改写：geoip.GeoInfo.Province；geoSyncTable 重写（发现=未入表求差，回写=逐 IP upsert）；geoSyncUpsert；geoSyncExistingIPs；startGeoSyncTimer；normalizeGeoSyncInterval；geoSyncOnDone 回写钩子（STEP3 注入）
- obs.PurgeTrafficCache；Server.geoSyncStop（停机关定时器）
### 偏差与现场记录
- 上报字段 rows_updated 改名 rows_upserted（语义=upsert geoip_list 行数）；数据库页消费文案在 STEP5 同步。
- 已入表 IP 不再被重发现（增量求差语义），冲突更新路径仅为防御；upsert 冲突测试改为直测 geoSyncUpsert。
- 与 STEP1 合并一次提交（见 STEP1 偏差记录）。
