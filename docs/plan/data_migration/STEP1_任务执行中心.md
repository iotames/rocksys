# STEP1：任务执行中心 internal/taskcenter + /admin/tasks 端点

状态：待实施

## 目标
新建纯内存任务执行中心（D20–D29）：全局单任务互斥、panic 收口、终态保留 100 条、ID={纪元}-{序号}、Progress 快照替换、Cancel 竞态语义；adminapi 挂 `/admin/tasks`（GET 列表）、`/admin/tasks/{id}`（GET 详情，404）、`/admin/tasks/{id}/cancel`（POST，404/终态不报错）。

## 改动文件清单
- 新增 `internal/taskcenter/taskcenter.go` + `taskcenter_test.go`
- 新增 `internal/adminapi/tasks.go`（HTTP 薄封装）
- 修改 `internal/adminapi/adminapi.go`（字段 tasks *taskcenter.Center + 路由注册；New 内构造）
- 修改 `cmd/rocksys/main.go`（无：中心在 adminapi.New 内自建，无需装配——保持零配置）

## 实施步骤（完成一项立即勾选保存）
- [ ] 定义 Status/Task/Progress 快照类型与 Center（Submit/Get/List/Cancel）
- [ ] Submit 全局互斥 + defer 收口（recover→failed、终态裁定、释放互斥、终态入列淘汰最旧）
- [ ] Get/List 快照原子读；Cancel 竞态语义（已终态返回终态不报错）
- [ ] adminapi tasks.go 三个 handler + 路由注册
- [ ] 单测：互斥拒绝、panic 收口后互斥可再获取、终态 100 条淘汰、Cancel 与完成竞态、Get/Cancel 404、迟到 setProgress 忽略、时间字段自动记录

## 验证
- 接手核实命令：`go test ./internal/taskcenter/ ./internal/adminapi/`
- 本步完整验证：
  - [ ] `go build ./... && go vet ./... && go test ./internal/taskcenter/ ./internal/adminapi/`

## 完成标准
- 上述单测全绿；vet 无新增告警；路由注册编译通过。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增文件：internal/taskcenter/taskcenter.go、internal/taskcenter/taskcenter_test.go、internal/adminapi/tasks.go
- 新增类型/函数：taskcenter.Center / Submit / Get / List / Cancel / SetProgress；adminapi.handleTasksList / handleTasksGet / handleTasksCancel
- 新增端点：GET /admin/tasks、GET /admin/tasks/{id}、POST /admin/tasks/{id}/cancel
### 偏差与现场记录
- （无）
