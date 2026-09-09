# STEP4：obs 读侧流量统计端点 + 缓存

状态：已实施

## 目标
按 PLAN §3.4：plugins/obs 新增 traffic.go、traffic_cache.go，admin.go 注册三端点：
- GET /admin/obs/traffic/summary?from=&to= → 标量+率（Go 算分母 0 防护）+ computed_at/cache_ttl
- GET /admin/obs/traffic/series?from=&to=&bucket=hour|day → 缺省自适应（跨度≤48h 用 hour）
- GET /admin/obs/traffic/geo?from=&to=&source=access|blocked
- 缓存：纯标准库 singleflight（mutex+map）+ 过期时刻表，key=端点+from+to+bucket/source；不设上限淘汰（边界已标注）
- 新配置 OBS_TRAFFIC_CACHE_TTL（时长默认 10m，0=禁用，conf.Register）
- 降级：obs 未启用 503；SHIELD_EVENT_LOG_ENABLED=false 拦截侧字段 null

## 改动文件清单
- plugins/obs/traffic.go（新）、traffic_cache.go（新）、admin.go（注册）
- conf 注册（沿 obs 既有配置注册模式）
- traffic_cache_test.go（命中/过期/TTL=0）

## 实施步骤（完成一项立即勾选保存）
- [x] traffic_cache.go + 单测 ✓（go test ./plugins/obs/ -run Cache）
- [x] traffic.go 三端点（参数校验/降级/null 语义）
- [x] OBS_TRAFFIC_CACHE_TTL 注册
- [x] admin.go 注册路由 ✓（go build，通过）

## 验证
- 接手核实命令：`go test ./plugins/obs/ && grep -n "traffic" plugins/obs/admin.go | head`
- 本步完整验证：
  - [x] `go test ./... && go vet ./...`（全过）
  - [x] dev 运行实测（bin/ 本地实例）：summary/series/geo 出数；二次请求 cache_hit=true 且 computed_at 不变；PUT OBS_TRAFFIC_CACHE_TTL=0 后每次变化（cache_ttl_sec=0）；恢复 600 生效
  - [x] OBS_ENABLED=false 热更 → 三端点 503 引导态；恢复 true → 200
  - [x] 顺带实测老库升级闭环（验收 §3）：缺列 warning → /admin/db/schema 5 条补列 diff → /admin/db/exec 补列 → 统计出数

## 完成标准
- 三端点可用、缓存语义正确、降级路径明确。

## 实施回填区
### 产物锚点清单（实施后核实）
- 新增端点：/admin/obs/traffic/{summary,series,geo}（main.go 注册，obs.PathTraffic*）
- 新增配置：OBS_TRAFFIC_CACHE_TTL（秒，默认 600，0=禁用）
- 新增文件：plugins/obs/traffic.go、traffic_cache.go、traffic_cache_test.go（4 用例：命中/过期/禁用/singleflight/失败不缓存）
- 注入：obs.SetShieldTable / obs.SetBlockAvailable（SHIELD_EVENT_LOG_ENABLED=false → 拦截侧 null）；EventRecorder.LoggingEnabled()
### 偏差与现场记录
- 本机 python3 为 WindowsApps 空壳导致一次装配补丁静默丢失（main.go 注册块/LoggingEnabled），已用真 python 重打并经运行时验证兜底——后续本会话一律用 `python`。
- 端口曾长期被陈旧实例占用造成"改动未生效"假象：验证前必须核对 /tmp/rocksys_dev.log 的启动时间与 netstat PID 一致。
