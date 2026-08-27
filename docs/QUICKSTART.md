# 快速开始

> 完整上手指引（构建 / 开发模式 / 运行 / WebUI / 验证代理）。由 README.md「快速开始」章节下沉而来。

## 1. 构建

**环境要求**：Go 1.25+（Linux / macOS / Windows 均可，产物为 Linux 目标时可直接交叉编译）。

```bash
# 同步依赖地基库（easyconf/easyserver/easydb）并构建
make build                        # 产物 bin/rocksys

# 查看版本（版本号 = 当前 git 最新 tag）
./bin/rocksys --version

# 或跳过 make deps，直接构建（不注入版本，--version 显示 dev）
go build -o bin/rocksys ./cmd/rocksys

# 发布打包：编译二进制 + 拷贝外挂资源（SQL/WAF 规则/可信代理）到 bin/hotscripts/
# 运行期外挂优先、内嵌兜底，改这些文件无需重新编译
make release

# 交叉编译生产产物（纯 Go 无 CGO，可直接产出目标平台二进制）
# 产物 bin/rocksys-linux-amd64、bin/rocksys-linux-arm64
make cross-build
```

> 构建产物为**单个可执行文件**，WebUI 管理控制台已 `go:embed` 内嵌在二进制中，零额外前端文件。

> **Makefile 仅支持 Linux**（纯 Unix 语法，Windows 原生 cmd 不支持）。Windows 下请经 WSL2 执行（`cd /mnt/d/.../rocksys && make xxx`），或直接用下方 go 命令。

## 开发模式：改前端免编译（-tags dev）

默认 WebUI 内嵌进二进制，改前端代码必须重新编译才能看到效果。开发模式用 `-tags dev` 编译：WebUI 改为**实时读 `webui/` 源码目录**，改 `index.html` / `assets/` 下文件后**刷新浏览器即见，无需重新编译**（新增文件需重启一次，路由在启动时注册）：

```bash
# 开发模式编译（WebUI 前端免编译热重载）
go build -tags dev -o bin/rocksys ./cmd/rocksys

# 运行（★ 必须在 bin/ 目录运行，工作目录 = bin/）
cd bin && ./rocksys

# 浏览器打开 http://127.0.0.1:19527/，改 webui/ 下文件 → 刷新即见效果
```

> `make dev` 一键完成编译 + 运行两步（仅 Linux/WSL2）。生产构建不加 dev tag，WebUI 照常内嵌，两种模式互不影响。

## 2. 运行

运行 RockSys 会同时出现 **3 个地址**，先看清各自职责：

```
客户端 ──HTTP──▶ 代理监听端口  ──转发──▶ 被代理的后端（upstream）
                 （默认 :8080）            （默认 http://127.0.0.1:9000 占位，需改为实际后端）
                      │
                      └── 管理接口 + WebUI 控制台（默认 127.0.0.1:19527，仅回环）
```

| 地址 | 默认值 | 是谁 | 谁访问 |
|------|--------|------|--------|
| 代理监听端口 | `:8080` | 对外收请求的入口 | 客户端/浏览器 |
| 被代理后端 | `http://127.0.0.1:9000` | 真正干活的业务服务（占位默认，需改为实际后端） | 只有代理转发给它 |
| 管理/WebUI | `127.0.0.1:19527` | 管理接口 + 图形控制台 | 运维（回环，不对外） |

```bash
# 最小运行态：裸反向代理
#   代理端口   = 默认 :8080        → 客户端访问 http://<host>:8080/
#   被代理后端 = http://127.0.0.1:9000   （实际业务服务，建议与代理端口不同，避免混淆）
#   WebUI      = http://127.0.0.1:19527/
./bin/rocksys --upstream http://127.0.0.1:9000

# 显式指定三个地址（最清晰）：
#   --listen    :8080                  代理对外端口（客户端访问 http://<host>:8080/）
#   --upstream  http://127.0.0.1:9000  被代理的后端（业务服务）
#   --admin     127.0.0.1:19527        管理/WebUI 地址（浏览器打开 http://127.0.0.1:19527/）
./bin/rocksys --listen :8080 --upstream http://127.0.0.1:9000 --admin 127.0.0.1:19527

# 指定配置文件（.env）
./bin/rocksys --config /etc/rocksys/rocksys.env
```

## 3. 打开管理控制台（WebUI）

浏览器访问 **`http://127.0.0.1:19527/`**（即 `--admin` 指定的地址）即打开图形化管理控制台（默认仅监听回环地址）。

首次使用：
1. 默认监听回环地址，本机访问免登录。
2. 默认落地「概览」页：查看网关状态、运行指标、HTTP 数据流（组件节点带开关）与服务总览。

## 4. 验证代理

```bash
# 8080 是代理端口；此请求经代理转发到后端（上例 :9000），返回后端响应原样
curl http://127.0.0.1:8080/hello
# → 上游响应原样返回
```
