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
#   make geoip       # 归位/下载 GeoLite2 mmdb 到 bin/geoip/（本地找得到就复用，找不到才下载；失败不阻断）
#   make zip         # 三平台发布包打包：cross-build + geoip + 外挂资源 → bin/rocksys-<版本>-<os>-<arch>.zip
#   make release     # 发布打包：编译二进制 + geoip + 拷贝外挂资源到 bin/hotscripts/（已存在文件跳过不覆盖）
#   make dev         # 开发模式：-tags dev 编译并在 bin/ 运行（WebUI 前端免编译热重载，改文件刷新即见）
#   make test        # 运行全部测试
#   make vet         # 静态检查
#   make gen-env     # 生成 bin/default.env 全量默认值快照（在 bin/ 目录运行，不删 .env）
#   make run         # 构建并在 bin/ 目录运行（工作目录=bin/，运行时文件落 bin/，绝不污染项目根目录）
#   make deploy      # 部署到远端服务器：build → 上传 bin/rocksys → systemctl restart（Linux/Mac/WSL；不支持 Windows）
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

# deploy 部署目标：SSH/SCP 登录名（Host 别名），环境变量优先，缺省 rocksys；
# 实际 IP/端口/密钥由 ~/.ssh/config 的 Host 条目解析，本 Makefile 不掺和。
ROCKSYS_SERVER ?= rocksys
# deploy 远端部署目录（相对 $HOME）：~/projects/rocksys/bin
REMOTE_DIR := projects/rocksys/bin

# GeoIP 数据（TRAFFIC_ANALYSIS）：make geoip 归位/下载 mmdb 到 bin/geoip/（运行期 GEOIP_MMDB_DIR
# 默认值即 geoip，相对工作目录 bin/ 解析，解压/部署后开箱即用）。
#   - 查找链：bin/geoip → bin（工作目录）→ . → ~/geoip，任一命中即复用（未归位则复制，不重复下载）
#   - 本地全无才下载（P3TERX/GeoLite.mmdb 镜像直链）；国内网络可指定代理：
#     make geoip GEOIP_PROXY=http://127.0.0.1:7897
#   - 下载失败仅告警不阻断（geo 为可选增强，缺失时运行期自动降级「未知」）
GEOIP_DIR ?= bin/geoip
GEOIP_BASE_URL ?= https://github.com/P3TERX/GeoLite.mmdb/releases/download/2026.09.07
GEOIP_FILES := GeoLite2-City.mmdb GeoLite2-Country.mmdb
GEOIP_PROXY ?=

.PHONY: all deps build cross-build geoip zip release deploy dev test vet gen-env run clean

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
# 实现在下方 copy_noclobber（逐文件拷贝、已存在即跳过）。

# 逐文件拷贝、目标已存在即跳过（不盲目覆盖）：bin/hotscripts/ 是运行期外挂资产，
# 用户可能已做本地个性化修改，反复发布必须保留（与 deploy 不触碰服务端 hotscripts/ 同一哲学）。
define copy_noclobber
	src=$(1); sub=$(2); \
	find $$src -type f | while read -r f; do \
		if [ "$$f" = "$$src" ]; then rel=$$(basename $$src); else rel=$${f#$$src/}; fi; \
		d=bin/hotscripts/$$sub/$$rel; \
		if [ -e "$$d" ]; then \
			echo "  跳过（已存在）: $$d"; \
		else \
			mkdir -p $$(dirname $$d); \
			cp $$f $$d; \
		fi; \
	done
endef

release-assets:
	@echo "==> 拷贝外挂资源到 bin/hotscripts/（已存在的文件跳过，不覆盖）"
	@mkdir -p bin/hotscripts
	@$(call copy_noclobber,sql,sql)
	@$(call copy_noclobber,plugins/shield/rules,rules)
	@$(call copy_noclobber,internal/netutil/trusted_proxies.txt,trusted_proxies)
	@echo "==> 发布包就绪"
	@echo "  外挂资源: $$(find bin/hotscripts -type f | wc -l | tr -d ' ') 个文件（位于 bin/hotscripts/）"

# geoip：归位/下载 mmdb 到 bin/geoip/（release/zip 前置依赖；mmdb 不入库，发布物内置开箱即用）。
# 查找链 bin/geoip → bin → . → ~/geoip 任一命中即复用（未归位则复制）；全无才下载，失败仅告警不阻断
# （geo 为可选增强，缺失时运行期自动降级「未知」）。国内网络：make geoip GEOIP_PROXY=http://127.0.0.1:7897
geoip:
	@mkdir -p $(GEOIP_DIR)
	@for f in $(GEOIP_FILES); do \
		found=""; \
		for d in $(GEOIP_DIR) bin . $$HOME/geoip; do \
			if [ -f "$$d/$$f" ]; then found="$$d/$$f"; break; fi; \
		done; \
		if [ -n "$$found" ]; then \
			echo "==> $$f: 使用本地 $$found"; \
			if [ "$$found" != "$(GEOIP_DIR)/$$f" ]; then cp "$$found" "$(GEOIP_DIR)/$$f"; echo "  已归位到 $(GEOIP_DIR)/"; fi; \
		else \
			echo "==> 本地无 $$f，开始下载（下载失败不影响发布）"; \
			curl -fL $(if $(GEOIP_PROXY),--proxy $(GEOIP_PROXY)) -o "$(GEOIP_DIR)/$$f.part" "$(GEOIP_BASE_URL)/$$f" \
				&& mv "$(GEOIP_DIR)/$$f.part" "$(GEOIP_DIR)/$$f" \
				&& echo "  已下载 $(GEOIP_DIR)/$$f" \
				|| { rm -f "$(GEOIP_DIR)/$$f.part"; echo "警告: $$f 下载失败，发布包将不含该文件（运行期 geo 自动降级为「未知」）" >&2; }; \
		fi; \
	done
	@ls -lh $(GEOIP_DIR)/ 2>/dev/null | grep mmdb || echo "  （$(GEOIP_DIR)/ 下暂无 mmdb 文件）"

release: build geoip release-assets
	@echo "  二进制: bin/rocksys"
	@echo "  GeoIP 数据: bin/geoip/（GEOIP_MMDB_DIR 默认值相对 bin/ 解析，开箱即用）"

# 三平台发布包打包：在 cross-build 裸产物 + geoip 数据 + 外挂资源基础上，为每个平台生成 zip。
# 产物：bin/rocksys-<版本>-<os>-<arch>.zip，解压即用（二进制 + hotscripts/ 外挂资源 + geoip/ 数据），
# 适合上传 GitHub Release（配合 .github/workflows/release.yml 打 tag 自动发布）。
zip: cross-build geoip release-assets
	@for t in $(CROSS_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		dir="rocksys-$(VERSION)-$$os-$$arch"; \
		rm -rf "bin/$$dir"; \
		mkdir -p "bin/$$dir"; \
		cp "bin/rocksys-$$os-$$arch$$ext" "bin/$$dir/rocksys$$ext"; \
		cp -r bin/hotscripts "bin/$$dir/"; \
		if ls $(GEOIP_DIR)/*.mmdb >/dev/null 2>&1; then \
			mkdir -p "bin/$$dir/geoip"; \
			cp -r $(GEOIP_DIR)/*.mmdb "bin/$$dir/geoip/"; \
		else \
			echo "==> 提示: 无 mmdb 文件，zip 不含 geoip/ 目录（运行期 geo 自动降级「未知」）"; \
		fi; \
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

# deploy：构建并部署到远端服务器（Linux/Mac/WSL 执行；依赖 ssh/scp，不支持 Windows）。
# 用法：make deploy（或 ROCKSYS_SERVER=<user@host别名> make deploy）。
# 流程：build → ELF 防呆校验 → 远端 GeoIP 数据检查同步 → mkdir 远端目录 → 上传临时文件 →
# 远端原子替换 + --version 回显验证 → systemctl restart → is-active 确认服务存活。
# 任何一步失败立即中止（geoip 同步除外：双缺仅告警不阻断，geo 为可选增强）。
# 关键点：先传临时名 rocksys.new 再 mv 原子替换——直接 scp 覆盖运行中的二进制会报
# Text file busy（ETXTBSY）；mv 是 rename，停机窗口压缩到 restart 一瞬。
# 注：仅上传二进制与缺失的 mmdb，不触碰服务端 hotscripts/（服务器侧资产，或含个性化配置，不随部署覆盖）。
deploy: build
	@echo "==> 校验产物为 Linux ELF（Mac 本机构建产物会被拦截，请经 WSL 构建）"
	@head -c 4 bin/rocksys | od -An -tx1 | grep -q '7f 45 4c 46' || \
		{ echo "错误: bin/rocksys 不是 Linux 二进制，已中止部署" >&2; exit 1; }
	@echo "==> 检查远端 GeoIP 数据（查找链 geoip/ → ./ → ~/geoip，缺则从本地 $(GEOIP_DIR)/ 同步）"
	@for f in $(GEOIP_FILES); do \
		if ssh $(ROCKSYS_SERVER) "test -f ~/$(REMOTE_DIR)/geoip/$$f || test -f ~/$(REMOTE_DIR)/$$f || test -f ~/geoip/$$f"; then \
			echo "==> $$f: 远端已存在，跳过"; \
		elif [ -f "$(GEOIP_DIR)/$$f" ]; then \
			echo "==> 远端缺 $$f，从本地 $(GEOIP_DIR)/ 同步"; \
			ssh $(ROCKSYS_SERVER) "mkdir -p ~/$(REMOTE_DIR)/geoip"; \
			scp -p "$(GEOIP_DIR)/$$f" "$(ROCKSYS_SERVER):~/$(REMOTE_DIR)/geoip/$$f" \
				|| echo "警告: $$f 同步失败（不阻断部署，远端 geo 降级为「未知」）" >&2; \
		else \
			echo "警告: 本地 $(GEOIP_DIR)/ 与远端均无 $$f，远端地理位置统计将降级为「未知」（可运行 make geoip 补齐后重部署）" >&2; \
		fi; \
	done
	@echo "==> 上传到 $(ROCKSYS_SERVER):~/$(REMOTE_DIR)/"
	@ssh $(ROCKSYS_SERVER) "mkdir -p ~/$(REMOTE_DIR)"
	scp -p bin/rocksys $(ROCKSYS_SERVER):~/$(REMOTE_DIR)/rocksys.new
	@echo "==> 远端替换并重启 rocksys"
	@ssh $(ROCKSYS_SERVER) "set -e; cd ~/$(REMOTE_DIR); \
		chmod +x rocksys.new; mv -f rocksys.new rocksys; \
		./rocksys --version; \
		systemctl restart rocksys; sleep 1; systemctl is-active rocksys"
	@echo "==> 部署完成: $(ROCKSYS_SERVER) 已重启运行 $(VERSION)"

clean:
	rm -rf bin
