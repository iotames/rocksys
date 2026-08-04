# RockSys 构建入口
#
# 依赖地基库（easyconf/easyserver/easydb）为独立 git 仓库，主模块经 go.mod replace
# 引用本地路径。三个子仓库的 origin 一律以 github.com/iotames 为准（githost/nas 仅作
# 私有 push 目标）。make deps 自动处理依赖：
#   - 子仓库目录缺失 → 从 github.com 拉取（clone 失败报错退出）
#   - 子仓库目录已存在 → 执行 git pull 同步（网络不可达时警告并继续，不阻塞构建）
#
# 注意：deps 使用 https 公共仓库（只读）clone/pull；push 请用
# `github:iotames/xxx.git` SSH 别名（配好权限、默认登录），本 Makefile 不做 push。
#
# 用法：
#   make deps        # 同步依赖仓库
#   make build       # 构建 bin/rocksys
#   make cross-build # 交叉编译生产产物（bin/rocksys-linux-amd64 / bin/rocksys-linux-arm64）
#   make test        # 运行全部测试
#   make vet         # 静态检查
#   make run         # 构建并运行

REPOS  := easyconf easyserver easydb
GITHUB := https://github.com/iotames

# 交叉编译目标（GOOS/GOARCH，纯 Go 无 CGO 可直接编译；modernc sqlite 为纯 Go 实现）
CROSS_TARGETS := linux/amd64 linux/arm64

.PHONY: all deps build cross-build test vet run clean

all: build

deps:
	@for d in $(REPOS); do \
		if [ -d "$$d/.git" ]; then \
			echo "==> $$d 已存在，git pull"; \
			timeout 20 git -C "$$d" pull --ff-only 2>/dev/null || echo "    warn: $$d pull 失败（网络不可达？），使用本地已有代码继续"; \
		else \
			echo "==> clone $$d from $(GITHUB)/$$d.git"; \
			git clone "$(GITHUB)/$$d.git" "$$d" || exit 1; \
		fi; \
	done

build: deps
	go build -o bin/rocksys ./cmd/rocksys

# 交叉编译：产物带平台后缀 bin/rocksys-<os>-<arch>，可拷贝到目标服务器直接运行。
cross-build: deps
	@mkdir -p bin
	@for t in $(CROSS_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "==> cross-build $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/rocksys-$$os-$$arch ./cmd/rocksys; \
	done
	@echo "==> 交叉编译产物:"; ls -lh bin/rocksys-*

test: deps
	go test ./...

vet: deps
	go vet ./...

run: build
	./bin/rocksys

clean:
	rm -rf bin
