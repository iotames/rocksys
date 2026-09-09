# STEP2：internal/geoip 包 + GEOIP_MMDB_DIR 配置 + 写时解析接线

状态：已实施

## 目标
按 PLAN §3.3：新包 internal/geoip（依赖 github.com/oschwald/maxminddb-golang/v2，D15）；GEOIP_MMDB_DIR 经 conf.Register（默认 geoip）；City/Country 两 mmdb 逐文件独立查找链（$GEOIP_MMDB_DIR → CWD → $HOME/geoip）；惰性加载含缺失结论缓存、warning 精确列缺失文件；obs OnDone / shield newEvent 各解析一次填列。解析失败/未加载列空串不阻断。

## 改动文件清单
- internal/geoip/geoip.go（新）+ geoip_test.go
- go.mod / go.sum（新增 v2 依赖）
- internal/conf 注册处（沿既有注册模式；GEOIP_MMDB_DIR）
- cmd/rocksys/main.go（装配：geoip Resolver 注入 obs 与 shield）
- plugins/obs/obs.go、plugins/shield/event_recorder.go（接 Resolver 填 country/city）

## 实施步骤（完成一项立即勾选保存）
- [x] go get github.com/oschwald/maxminddb-golang/v2（国内镜像优先） ✓（go test ./... && go vet ./...，通过）✓（go build ./...，通过）
- [x] internal/geoip：Lookup(ip) (country, city string)；逐文件查找链；惰性加载+缺失结论缓存；warning 日志（缺哪个文件、去哪下载、放置目录、重启生效） ✓（go test ./... && go vet ./...，通过）
- [x] 单测：查找链逐文件解析、缺失降级、并发安全（7 用例，子 Agent 完成）✓（go test ./internal/geoip/，通过）
- [x] GEOIP_MMDB_DIR 注册 + 装配接线（obs/shield 注入） ✓（go test ./... && go vet ./...，通过）✓（go build -tags dev -o bin/rocksys ./cmd/rocksys，通过）
- [x] obs/shield 写时解析填列 ✓（go test ./... && go vet ./...，通过）

## 验证
- 接手核实命令：`go test ./internal/geoip/ && grep -rn "GEOIP_MMDB_DIR" internal/ plugins/ cmd/ | head`
- 本步完整验证：
  - [ ] `go test ./... && go vet ./...`
  - [x] dev 构建后 bin/ 冒烟：GEOIP_MMDB_DIR 已入 default.env（注册成功）、启动无异常（端口占用为既有旧实例，与新代码无关）
  - （真实 mmdb 落值验证无 fixture，归入 STEP7/9 浏览器环节；无 mmdb 降级路径由单测覆盖；已知边界照 PLAN §5）

## 完成标准
- 无 mmdb 降级不阻断；有 mmdb 写时落值；配置项注册并可热更。

## 实施回填区
### 产物锚点清单（实施后核实）
- 新增包：internal/geoip（geoip.go/geoip_test.go/geoip_integration_test.go，子 Agent 产出）；接口 NewResolver(dir)/Lookup(ip)(country,city)/Ready()
- 新增配置项：GEOIP_MMDB_DIR（默认 geoip，cmd/rocksys/main.go 注册）
- 接线：cmd/rocksys/main.go 构造共享 geoRes → obs.New 后 obsMw.SetGeoip(geoRes)、shield recorder.SetGeoip(geoRes)；obs OnDone/shield Record（newGeoEvent）写时解析
- 依赖：go.mod 新增 github.com/oschwald/maxminddb-golang/v2 v2.6.0（x/sys 连带 v0.46→v0.47）
### 偏差与现场记录
- NewResolver 不收 logger 参数（沿仓库 log 包级 API 惯例）；Ready() 触发惰性定位防"文件已就位未加载"误报（子 Agent 报告，两处偏差合理）。
- 真实 mmdb 运行时验证缺 fixture，geo 落值留待 STEP7/9；无 mmdb 降级已由单测覆盖。
