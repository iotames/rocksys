# STEP3：schedule_list 装配登记 + GET /admin/schedule/list

状态：待实施

## 目标
装配期对 schedule_list 按 name upsert 登记（可配型 4 + 系统级若干，D19/D20）；系统级行被改则重启按 name 重置；新增只读端点 GET /admin/schedule/list（行内附 enabled，服务端读 config_key 对应 easyconf 当前值）；geoip_sync 执行结束回写 last_run_at/last_status/last_message（D21，单点在 geoSyncAll）。

## 改动文件清单
- 新增 cmd/rocksys/schedule.go（登记器 + 端点 handler + 状态回写器）
- 新增 cmd/rocksys/schedule_test.go
- 修改 cmd/rocksys/main.go（装配接线：建表、登记、端点注册、向 geoSyncAll 注入回写器）

## 实施步骤
- [ ] schedule.go：EnsureTable + Upsert（装配期）+ ResetSystemRows（重启重置）+ List（含 enabled 计算）+ UpdateRunStatus
- [ ] 登记清单：configurable=obs_log_prune/shield_event_prune/shield_auto_ban/geoip_sync；system=hotswap 文件监控、conf 配置轮询、registry 心跳扫描、dispatch 健康检查、mq 投递轮询、shield IP 快照 TTL 重建、shield 事件 flush
- [ ] geoip_sync 行 remark 按 mmdb 是否加载注明依赖状态
- [ ] GET /admin/schedule/list 注册（只读）
- [ ] geoSyncAll 收口回写（成功/失败/进行中拒绝不写）
- [ ] 单测：upsert 幂等、系统级重置、enabled 计算纯函数

## 验证
- 接手核实命令：`go test ./cmd/rocksys/ -run TestSchedule -count=1`
- 本步完整验证：
  - [ ] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys`
  - [ ] `go test ./cmd/rocksys/ -count=1`
  - [ ] 运行期手测：`cd bin && ./rocksys.exe` 后 `curl http://127.0.0.1:19527/admin/schedule/list` 返回登记行

## 完成标准
- 端点返回可配型 4 + 系统级 7 行；enabled 与配置实值一致；geoip_sync 手动同步后 last_run_at/last_status 更新。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增端点：GET /admin/schedule/list
- 新增函数：ScheduleRegistry（EnsureTable/Upsert/ResetSystem/List/UpdateRunStatus）
### 偏差与现场记录
- （无）
