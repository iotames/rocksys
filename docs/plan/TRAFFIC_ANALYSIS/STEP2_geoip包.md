# STEP2：internal/geoip 包 + GEOIP_MMDB_DIR 配置 + 写时解析接线

状态：待实施

## 目标
按 PLAN §3.3：新包 internal/geoip（依赖 github.com/oschwald/maxminddb-golang/v2，D15）；GEOIP_MMDB_DIR 经 conf.Register（默认 geoip）；City/Country 两 mmdb 逐文件独立查找链（$GEOIP_MMDB_DIR → CWD → $HOME/geoip）；惰性加载含缺失结论缓存、warning 精确列缺失文件；obs OnDone / shield newEvent 各解析一次填列。解析失败/未加载列空串不阻断。

## 改动文件清单
- internal/geoip/geoip.go（新）+ geoip_test.go
- go.mod / go.sum（新增 v2 依赖）
- internal/conf 注册处（沿既有注册模式；GEOIP_MMDB_DIR）
- cmd/rocksys/main.go（装配：geoip Resolver 注入 obs 与 shield）
- plugins/obs/obs.go、plugins/shield/event_recorder.go（接 Resolver 填 country/city）

## 实施步骤（完成一项立即勾选保存）
- [ ] go get github.com/oschwald/maxminddb-golang/v2（国内镜像优先）✓（go build ./...，通过）
- [ ] internal/geoip：Lookup(ip) (country, city string)；逐文件查找链；惰性加载+缺失结论缓存；warning 日志（缺哪个文件、去哪下载、放置目录、重启生效）
- [ ] 单测：查找链逐文件解析、缺失降级（临时目录构造，不依赖真实 mmdb 时用 skip/假数据按库测试能力取舍）✓（go test ./internal/geoip/）
- [ ] GEOIP_MMDB_DIR 注册 + 装配接线（obs/shield 注入）✓（go build -tags dev -o bin/rocksys ./cmd/rocksys，通过）
- [ ] obs/shield 写时解析填列

## 验证
- 接手核实命令：`go test ./internal/geoip/ && grep -rn "GEOIP_MMDB_DIR" internal/ plugins/ cmd/ | head`
- 本步完整验证：
  - [ ] `go test ./... && go vet ./...`
  - [ ] dev 构建后 bin/ 运行（无 mmdb）→ 日志 warning 含缺失文件名；有 mmdb 时 access_log/shield_event 新行 country/city 落值

## 完成标准
- 无 mmdb 降级不阻断；有 mmdb 写时落值；配置项注册并可热更。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 新增包：internal/geoip；接口：Resolver.Lookup(ip) (country, city string)
- 新增配置项：GEOIP_MMDB_DIR（默认 geoip）
### 偏差与现场记录
- （无）
