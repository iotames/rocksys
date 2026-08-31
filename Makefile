# RockSys 构建入口
#
# 依赖地基库（easyconf/easyserver/easydb）为独立 git 仓库，主模块经 go.mod replace
# 引用本地路径。三个子仓库的 origin 一律以 github.com/iotames 为准（githost/nas 仅作
# 私有 push 目标）。make deps 自动处理依赖：
#   - 子仓库目录缺失 → 从 github.com 拉取（clone 失败报错退出）
#   - 子仓库目录已存在 → 忽略
#
# 注意：deps 使用 https 公共仓库（只读）clone/pull；push 请用
# `github:iotames/xxx.git` SSH 别名（配好权限、默认登录），本 Makefile 不做 push。
#
# 用法：
#   make deps        # 同步依赖仓库
#   make build       # 构建 bin/rocksys
#   make cross-build # 交叉编译生产产物（bin/rocksys-<os>-<arch>[.exe]，含 linux amd64/arm64、windows amd64）
#   make zip         # 三平台发布包打包：cross-build + 外挂资源 → bin/rocksys-<版本>-<os>-<arch>.zip（可上传 GitHub Release）
#   make release     # 发布打包：编译二进制 + 拷贝外挂资源到 bin/hotscripts/（SQL/WAF规则/可信代理，可运行时热修改）
#   make dev         # 开发模式：-tags dev 编译并在 bin/ 运行（WebUI 前端免编译热重载，改文件刷新即见）
#   make test        # 运行全部测试
#   make vet         # 静态检查
#   make gen-env     # 生成 bin/default.env 全量默认值快照（在 bin/ 目录运行，不删 .env）
#   make run         # 构建并在 bin/ 目录运行（工作目录=bin/，运行时文件落 bin/，绝不污染项目根目录）
#
# 注：Makefile 为纯 Unix 语法，Windows 原生 cmd 不支持；请经 WSL2 执行（cd /mnt/d/.../rocksys && make xxx）。

REPOS  := easyconf easyserver easydb
GITHUB := https://github.com/iotames

# 主项目版本号：取当前 git 最新 tag（git describe --tags --abbrev=0）。
#   - 无任何 tag          → dev
#   - 当前提交恰为 tag     → 该 tag（如 v1.5.0）
#   - tag 之后有提交       → tag-dev（如 v1.5.0-dev）
# 注入 rocksys/cmd/rocksys.Version/BuildTime，供 ./rocksys --version 展示。
VERSION ?= $(shell \
  tag=$$(git describe --tags --abbrev=0 2>/dev/null); \
  if [ -z "$$tag" ]; then echo "dev"; \
  elif git describe --tags --exact-match 2>/dev/null > /dev/null 2>&1; then echo "$$tag"; \
  else echo "$$tag-dev"; fi \
)
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%S+08:00' -d '+8 hours')
# 主包符号挂载在 main 下（命令行构建模式），-X 用 main.Version/main.BuildTime。
LD_FLAGS := -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)

# 交叉编译目标（GOOS/GOARCH，纯 Go 无 CGO 可直接编译；modernc sqlite 为纯 Go 实现）
# windows 目标产物自动追加 .exe 后缀
CROSS_TARGETS := linux/amd64 linux/arm64 windows/amd64

.PHONY: all deps build cross-build zip release dev test vet gen-env run clean

all: build

deps:
	@for d in $(REPOS); do \
		if [ -d "$$d/.git" ]; then \
			echo "==> $$d 已存在。跳过......"; \
		else \
			echo "==> clone $$d from $(GITHUB)/$$d.git"; \
			git clone "$(GITHUB)/$$d.git" "$$d" || exit 1; \
		fi; \
	done

# build：CGO_ENABLED=0 强制静态链接（与 cross-build 一致；全项目纯 Go 依赖——modernc sqlite、
# mysql/pq 驱动均无 cgo，net 回落纯 Go resolver），产物可拷到任意 Linux 直接运行，无需匹配 glibc。
build: deps
	CGO_ENABLED=0 go build -ldflags "$(LD_FLAGS)" -v -o bin/rocksys ./cmd/rocksys

# 交叉编译：产物带平台后缀 bin/rocksys-<os>-<arch>，可拷贝到目标服务器直接运行。
cross-build: deps
	@mkdir -p bin
	@for t in $(CROSS_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "==> cross-build $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $(LD_FLAGS)" -o bin/rocksys-$$os-$$arch$$ext ./cmd/rocksys; \
	done
	@echo "==> 交叉编译产物:"; ls -lh bin/rocksys-*

# 拷贝外挂资源到 bin/hotscripts/（HOT_SCRIPTS_DIR 默认值），供 release/zip 复用。
# 外挂资源源 → 目标（运行期外挂优先、内嵌兜底，改文件无需重新编译）：
#   sql/                              → bin/hotscripts/sql/          （mysql/postgres/sqlite 三方言 SQL 脚本）
#   plugins/shield/rules/             → bin/hotscripts/rules/         （WAF 规则 7 个 txt 文件）
#   internal/netutil/trusted_proxies.txt → bin/hotscripts/trusted_proxies/（可信代理列表）
release-assets:
	@echo "==> 拷贝外挂资源到 bin/hotscripts/"
	@mkdir -p bin/hotscripts/sql bin/hotscripts/rules bin/hotscripts/trusted_proxies
	@cp -r sql/* bin/hotscripts/sql/
	@cp -r plugins/shield/rules/* bin/hotscripts/rules/
	@cp internal/netutil/trusted_proxies.txt bin/hotscripts/trusted_proxies/
	@echo "==> 发布包就绪"
	@echo "  外挂资源: $$(find bin/hotscripts -type f | wc -l | tr -d ' ') 个文件（位于 bin/hotscripts/）"

release: build release-assets
	@echo "  二进制: bin/rocksys"

# 三平台发布包打包：在 cross-build 裸产物 + 外挂资源基础上，为每个平台生成 zip。
# 产物：bin/rocksys-<版本>-<os>-<arch>.zip，解压即用（目录内含二进制 + hotscripts/ 外挂资源），
# 适合上传 GitHub Release（配合 .github/workflows/release.yml 打 tag 自动发布）。
zip: cross-build release-assets
	@for t in $(CROSS_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		dir="rocksys-$(VERSION)-$$os-$$arch"; \
		rm -rf "bin/$$dir"; \
		mkdir -p "bin/$$dir"; \
		cp "bin/rocksys-$$os-$$arch$$ext" "bin/$$dir/rocksys$$ext"; \
		cp -r bin/hotscripts "bin/$$dir/"; \
		cd bin && rm -f "$$dir.zip" && zip -rq "$$dir.zip" "$$dir" && cd ..; \
		rm -rf "bin/$$dir"; \
		echo "==> 打包完成: bin/$$dir.zip"; \
	done
	@ls -lh bin/rocksys-$(VERSION)-*.zip

# 开发模式：-tags dev 编译并在 bin/ 目录运行（工作目录=bin/）。
# WebUI 由 go:embed 切换到 os.DirFS("../webui") 实时读磁盘，改前端代码刷新浏览器即见，
# 无需重新编译、无需重启。生产构建（make build/run/cross-build）不加 dev tag，不受影响。
# CGO_ENABLED=0 与 build 一致（静态链接；dev 不跑 -race，无 cgo 需求）。
dev: deps
	CGO_ENABLED=0 go build -tags dev -ldflags "$(LD_FLAGS)" -o bin/rocksys ./cmd/rocksys
	cd bin && ./rocksys

test: deps
	go test ./...

vet: deps
	go vet ./...

# 生成 bin/default.env 全量默认值快照（所有已注册配置项默认值+注释；不删除 bin/.env）。
# ★ 红线：必须在 bin/ 目录运行（工作目录=bin/），default.env 才落在 bin/ 下；
#   禁止在项目根目录运行（会在根目录残留运行时文件）。gen-env 依赖 build 产物并 cd bin 执行。
gen-env: build
	cd bin && ./rocksys --gen-env

# ★ 红线：run 必须进入 bin/ 目录运行（工作目录=bin/），运行时文件（.env/default.env/logs/*.db）
#   跟随工作目录落在 bin/，绝不污染项目根目录。禁止在项目根目录直接执行 ./bin/rocksys。
run: build
	cd bin && ./rocksys

clean:
	rm -rf bin
