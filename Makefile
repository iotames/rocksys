# RockSys 构建入口
#
# 依赖地基库（easyconf/easyserver/easydb）为独立 git 仓库，主模块经 go.mod replace
# 引用本地路径。make deps 自动处理依赖：
#   - 子仓库目录缺失 → 从 github.com 拉取（clone 失败报错退出）
#   - 子仓库目录已存在 → 执行 git pull 同步（网络不可达时警告并继续，不阻塞构建）
#
# 用法：
#   make deps    # 同步依赖仓库
#   make build   # 构建 bin/rocksys
#   make test    # 运行全部测试
#   make vet     # 静态检查
#   make run     # 构建并运行

REPOS  := easyconf easyserver easydb
GITHUB := https://github.com/iotames

.PHONY: all deps build test vet run clean

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

test: deps
	go test ./...

vet: deps
	go vet ./...

run: build
	./bin/rocksys

clean:
	rm -rf bin
