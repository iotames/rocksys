# 项目目录结构（v3：扁平极简）

## 目录树

```
rocksys/
├── go.mod                        # 主模块（replace 引用 ./easyserver ./easyconf）
├── cmd/
│   ├── rocksys/                  # 底座唯一入口（必需）：装配 engine + 注册挂件
│   └── rockctl/                  # 运维命令行：在线热操作配置与开关
├── easyserver/                   # 独立子模块：HTTP 服务器框架 + hotswap 工具（含 TCP/WS 能力，但底座仅用 HTTP 转发）
├── easyconf/                     # 独立子模块：配置工具
├── internal/                     # ★ 框架私有（不可脱离、外部不可 import）
│   ├── engine/                   # 反向代理引擎：转发/超时/默认upstream
│   ├── chain/                    # 转发链编排 + 中间件接口
│   ├── dataflow/                 # 请求级数据流：trace_id/三时间戳/租户（串联）
│   ├── hotswap/                  # ★ 生产热运维引擎：配置热更/组件热切/脚本热载
│   ├── adminapi/                 # Admin API handler（回环地址，不对外网）
│   └── conf/                     # 底座配置封装（基于 easyconf）
├── plugins/                      # ★ 可选挂件（默认全关，可热插拔，可独立演进）
│   ├── shield/                   # L1 防护（转发链中间件）
│   ├── dispatch/                 # L2 路由分发（转发链中间件）
│   ├── result/                   # L3 结果处理（转发链中间件）
│   ├── trace/                    # trace_id 透传（转发链中间件）
│   ├── script/                   # RockScript：Lua 策略引擎（内嵌组件）
│   ├── config/                   # RockConfig：配置/热重载（内嵌组件）
│   ├── obs/                      # RockObs：日志/统计（内嵌组件）
│   ├── registry/                 # RockRegistry：服务注册（独立进程组件）
│   ├── auth/                     # RockAuth：认证/租户（独立进程组件）
│   ├── mq/                       # RockMQ：异步消息（独立进程组件）
│   └── object/                   # RockObject：对象存储（独立进程组件）
├── sdk/
│   └── python/                   # RockBiz SDK（独立发布给 stbiz_*）
├── contracts/
│   ├── openapi/                  # HTTP 契约（一等公民）
│   └── proto/                    # gRPC 契约（二等公民，可选）
├── examples/
│   └── stbiz_hello/              # 最小业务微服务模板
└── docs/
    ├── ARCHITECTURE.md
    └── PROJECT_STRUCTURE.md
```

## 1. 只有三层，无多余嵌套

| 层 | 目录 | 性质 |
|----|------|------|
| 地基库 | 根下 `easyserver/`、`easyconf/` | 独立子模块，可脱离复用 |
| 框架私有 | `internal/`（engine/chain/dataflow/hotswap/conf 平铺） | 不可关、外部不可 import |
| 可选挂件 | `plugins/`（11 个平铺目录） | 默认全关，可热开关 |

没有 `libs/`、没有 `internal/core/`、没有 `middleware/`/`components/` 之分。

## 2. 挂件统一 plugins/，不再分目录

架构文档中"中间件"与"组件"的区别仅是**挂载类型**，不再是目录：
- 转发链中间件（`shield/dispatch/result/trace`）：挂在 chain 的 head/middle/tail。
- 内嵌组件（`script/config/obs`）：编译进底座，经开关启用。
- 独立进程组件（`registry/auth/mq/object`）：独立部署，SDK/适配器对接。

统一内部约定（每个挂件一致）：

```
xxx/
├── xxx.go        # 接口 + SPI 扩展点 + 开关注册 Register()/Unregister()
├── builtin/      # 默认自包含实现
└── adapter/      # 第三方对接适配器（可选，按需添加）
```

## 3. 可复用性：哪些可脱离框架主体

| 包 | 可脱离？ | 形态 |
|----|---------|------|
| `easyserver` | ✅ 完全独立 | 独立 git 仓库/子模块（本就是开源框架） |
| `easyconf` | ✅ 完全独立 | 独立 git 仓库/子模块（本就是工具库） |
| `easydb` | ✅ 完全独立 | 独立 git 仓库/子模块（本就是数据操作库） |
| `sqlfiles`（根目录 `sqlfiles.go`） | ✅ 独立 | 编译期嵌入 `sql/` 目录的 embed 包 |
| `contracts` | ✅ 独立 | 独立契约仓库，对外发布版本 |
| `sdk/python` | ✅ 独立 | 独立发布，业务微服务引用 |
| `plugins/*` | ✅ 可独立演进 | 初期随框架，接口稳定后拆独立仓库 |
| `internal/*` | ❌ 不可脱离 | 框架私有，外部禁止 import |

## 3.5 数据访问层（easydb + SQL 脚本外置）

- 根目录 `sql/<dbtype>/`：**项目所有数据库查询语句的统一存放目录**（sqlite/mysql/postgres 方言分目录），
  编译期经 `sqlfiles` 包 embed 嵌入二进制，默认零配置可运行。
- `internal/hotswap/script.go`：`ScriptDir` 逐级加载机制——运行时外挂统一根目录 `HOT_SCRIPTS_DIR`（默认 `hotscripts/`）下各挂件子目录（`sql/`、`rules/`、`trusted_proxies/`）优先，
  找不到再回退编译期嵌入文件；改 SQL 无需重新编译。
- `internal/db`：统一数据访问层，数据操作以 easydb 为主，封装 `SQLSource` 接口；
  切换数据库驱动时若 `sql/<dbtype>/` 缺脚本则直接报错。
- 底座（反向代理转发引擎）**不直连业务数据库**（架构红线），本层仅服务可插拔组件（mq 等）。

## 4. ★ 生产热运维引擎（hotswap）

- `easyserver/hotswap`：底层热加载工具（文件/脚本/embed 原子替换）。
- `internal/hotswap`：生产运维引擎，统一承载三类热操作（组件热切）与文件逐级加载（ScriptDir）：

| 操作 | 能力 | 命令 |
|------|------|------|
| 配置热更 | 在线改路由/限流/开关，不重启 | `rockctl config set` |
| 组件热开关 | 在线挂载/摘除，原子切换 + 排空 | `rockctl switch on/off` |
| 紧急摘除 | 故障组件一键热关，转发链照常 | `rockctl switch off <comp>` |
| 脚本热载 | RockScript 策略热发布 / 回滚 | `rockctl script publish` |
| 文件逐级加载 | 外置目录优先、嵌入兜底（SQL/JSON 等纯文本） | `internal/hotswap/script.go` |

> 组件热切与 ScriptDir 属同一类目（程序运行时的实时操作管理），同包不同文件。
> **第一原则「热更即持久化」**：所有运行期热操作（配置热更/组件开关/脚本热载）立即生效；配置类变更同步写回配置文件（`.env` / `--config`），重启后状态保留。

## 5. 串联机制落地

| 串联机制 | 目录 |
|---------|------|
| DataFlow | `internal/dataflow` |
| trace_id 透传 | `plugins/trace` |
| 配置/开关下发通道 | `internal/hotswap` + `plugins/config` |
| 数据访问层 | `internal/db` + `easydb` + `sql/` |
| 工作池 | `internal/workpool` |
| 业务内网总线 | `sdk/python` |
| 日志聚合 | `plugins/obs` |

## 6. 模块关系

- 主模块 `go.mod` `replace` 引用 `./easyserver`、`./easyconf`、`./easydb`（本地开发用源码，发布用版本）。
- `stbiz_*` 是独立仓库独立 CI，本仓库只提供 `sdk/python` + `examples/stbiz_hello` 模板。
