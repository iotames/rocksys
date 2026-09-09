# STEP4：obs 读侧流量统计端点 + 缓存

状态：待实施

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
- [ ] traffic_cache.go + 单测 ✓（go test ./plugins/obs/ -run Cache）
- [ ] traffic.go 三端点（参数校验/降级/null 语义）
- [ ] OBS_TRAFFIC_CACHE_TTL 注册
- [ ] admin.go 注册路由 ✓（go build，通过）

## 验证
- 接手核实命令：`go test ./plugins/obs/ && grep -n "traffic" plugins/obs/admin.go | head`
- 本步完整验证：
  - [ ] `go test ./... && go vet ./...`
  - [ ] dev 运行 curl 三端点出数；同范围二次请求 computed_at 不变；TTL=0 每次变化

## 完成标准
- 三端点可用、缓存语义正确、降级路径明确。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 新增端点：/admin/obs/traffic/{summary,series,geo}
- 新增配置：OBS_TRAFFIC_CACHE_TTL（默认 10m）
- 新增类型：trafficCache（singleflight+TTL）
### 偏差与现场记录
- （无）
