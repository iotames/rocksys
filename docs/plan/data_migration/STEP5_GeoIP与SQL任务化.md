# STEP5：GeoIP 同步任务化 + SQL 后台执行

状态：已实施

## 目标
- geoip_sync（cmd/rocksys/main.go 装配处）：POST 改为向任务中心 Submit（CreatedBy=geoip_sync），立即返回任务 ID；geoSyncAll 逻辑零改动；成功后 PurgeTrafficCache 保持。
- /admin/db/exec（dbschema.go）：请求体加 `background:true` 时提交任务中心（CreatedBy=sql_exec），默认同步直返现状不变；后台执行同样落 sql_exec_log 审计。

## 改动文件清单
- 修改 `cmd/rocksys/main.go`（geoip_sync handler）
- 修改 `internal/adminapi/dbschema.go`（exec 后台分支）
- 测试：adminapi 侧后台执行单测

## 实施步骤（完成一项立即勾选保存）
- [x] adminapi 暴露 SubmitTask 帮助方法（供 main.go 装配处提交任务）
- [x] geoip_sync 任务化（进度=表计数快照、Result=报告文本）
- [x] exec background 分支（任务内复用既有逐条执行与审计逻辑）
- [x] 单测：exec background 提交→轮询拿到逐条结果；互斥拒绝文案

## 验证
- 接手核实命令：`go test ./internal/adminapi/ -run TestDBExec && go build ./...`
- 本步完整验证：
  - [x] `go build -tags dev -o bin/rocksys ./cmd/rocksys && go vet ./... && go test ./internal/adminapi/ ./cmd/rocksys/`（全绿，2026-09-15）

## 完成标准
- 构建、vet、单测全绿；>15 秒同步场景的真实超时解除验证归 STEP7 浏览器终验。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增函数：AdminServer.SubmitTask、handleDBExec 后台分支、main.go geoip_sync 任务提交
### 偏差与现场记录
- SubmitTask 已在 STEP1 落地为 adminapi 公开方法，本步直接复用。
- geoip_sync 响应结构变更为 {ok, task_id}（前端同批改造，STEP6 对齐）；报告文本经任务 Progress.Detail 透出。
- 后台执行审计 Source 标记 webui-background，与同步路径区分；同步路径互斥行为不变。
