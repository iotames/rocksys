# STEP2：CONF_DIR 注册 + 数据源管理（internal/adminapi/dsn.go）

状态：待实施

## 目标
注册全局配置 `CONF_DIR`（默认 conf，相对工作目录）；`internal/adminapi/dsn.go` 实现 easydb/dsn 底座接入：GET/POST /admin/db/dsn（列表脱敏/添加）、/admin/db/dsn/delete、/admin/db/dsn/test（连通测试）；持久化 `<CONF_DIR>/dsn.json`（懒创建，每次读写实时取 CONF_DIR 生效值）；main.go 装配注册 CONF_DIR；main_test.go 配置断言清单同步。

## 改动文件清单
- 新增 `internal/adminapi/dsn.go` + `dsn_test.go`
- 修改 `internal/adminapi/adminapi.go`（路由注册 + confMgr 指针字段；New 处注册 CONF_DIR 更合适——见实施）
- 修改 `cmd/rocksys/main.go`（CONF_DIR 注册，靠近 HOT_SCRIPTS_DIR）
- 修改 `cmd/rocksys/main_test.go`（断言清单加 CONF_DIR）

## 实施步骤（完成一项立即勾选保存）
- [ ] main.go 注册 CONF_DIR（title/usage 注明 dsn.json 位置语义）+ main_test.go 断言
- [ ] adminapi dsn.go：存储包装（每次读写按 CONF_DIR 当前值 NewDsnConf）、脱敏函数、4 个 handler + 路由
- [ ] 校验链：驱动已注册 / Name 唯一 / DSN 重复 / MySQL 密码 @（复用 dsn 包）+ 测试端点 sql.Open+Ping+版本
- [ ] 单测：CRUD 往返、脱敏形态、重复拦截、懒创建目录与文件

## 验证
- 接手核实命令：`go test ./internal/adminapi/ -run TestDsn && go test ./cmd/rocksys/ -run TestConf`
- 本步完整验证：
  - [ ] `go build ./... && go vet ./... && go test ./internal/adminapi/ ./cmd/rocksys/`

## 完成标准
- 单测全绿；bin/default.env 同步出 CONF_DIR（运行期自动，构建即验）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增文件：internal/adminapi/dsn.go、internal/adminapi/dsn_test.go
- 新增配置项：CONF_DIR（默认 conf）
- 新增端点：GET/POST /admin/db/dsn、POST /admin/db/dsn/delete、POST /admin/db/dsn/test
- 新增测试：TestDsn*、cmd/rocksys 配置断言
### 偏差与现场记录
- （无）
