# RockSys 磐石

**极简增强式反向代理底座**：默认一台 HTTP 反向代理（等同极简 NGINX `proxy_pass`），能力按需挂载、全部可热开关。任何组件掉链子，紧急关闭，转发依旧。

> 核心哲学：**只有转发是必须的**。其余一切皆是可选增强，默认全关，可随时热插拔。

---

## 产品功能

### 底座（必需，不可关）

| 能力 | 说明 |
|------|------|
| 反向代理引擎 | 接收全部 HTTP 请求 → 转发 → 回传响应，协议级纯转发 |
| 转发超时 | 慢后端不挂死代理连接（默认 5s，可配置） |
| 开关机制 | 中间件/组件在线挂载、摘除、原子切换、排空（hotswap） |
| 三层时间戳 | 防护/业务/总耗时精确分解（`ShieldMs + BizMs ≈ TotalMs`） |
| trace_id 透传 | 入口生成唯一标识，贯穿全链路 |

### 可选增强挂件（默认全关，可热开关）

| 挂件 | 作用 | 挂载 |
|------|------|------|
| **shield** | L1 防护：IP 黑白名单、路径/UA 规则、令牌桶限流、WAF（SQL/XSS/路径遍历/风险路径/爬虫 UA，规则外置文件可热改） | Head |
| **trace** | trace_id 透传 | Head |
| **auth** | JWT 认证 | Head |
| **dispatch** | L2 路由分发：Radix Tree 前缀树（支持参数 `:id`、通配 `*`、最长匹配）、节点组、平滑加权轮询 / 一致性哈希、主动健康检查、高优/备份节点 | Middle |
| **rewrite** | 转发前改写 URI / 请求头 | Middle |
| **script** | RockScript：Lua 策略引擎（网关策略，不承载业务逻辑） | Middle |
| **obs** | RockObs：访问日志（异步落盘）+ 指标聚合 + 查询 API | Tail |
| **copy** | 请求抄送：复制流量异步发送到 shadow 后端（审计/影子验证） | Tail |
| **result** | L3 结果处理：响应封装 / 字段脱敏 | Tail |
| **config** | RockConfig：配置热更下发 | 独立组件 |
| **registry** | RockRegistry：服务注册与发现 | 独立组件 |
| **object** | RockObject：对象存储（S3 兼容） | 独立组件 |
| **mq** | RockMQ：异步消息（Outbox 模式） | 独立组件 |

### 降级链（高可用的真正含义）

```
全量能力 ─▶ 关L3 ─▶ 关L2 ─▶ 关L1 ─▶ 裸反向代理（永远可达）
```

任何一环故障 → 热关闭 → 请求绕过该环直通下一级。转发行为永不中断。

---

## 快速开始

### 编译构建

```bash
# 同步依赖地基库（easyconf/easyserver/easydb）并构建
make build              # 产物 bin/rocksys

# 或直接构建
go build -o bin/rocksys ./cmd/rocksys
```

### 启动

```bash
# 最小运行态：裸反向代理（无需配置文件、无需数据库）
./bin/rocksys --upstream http://127.0.0.1:8080

# 指定监听端口与默认后端
./bin/rocksys --listen :8080 --upstream http://127.0.0.1:9000

# 指定配置文件（.env）
./bin/rocksys --config .env
```

启动即代理：`http://<host>:8080/` 收到的请求全部转发到默认后端。

### 验证

```bash
curl http://127.0.0.1:8080/hello
# → 上游响应原样返回
```

---

## 配置

所有配置支持三种来源（优先级从高到低）：**命令行参数 > 环境变量 > `.env` 配置文件**。配置文件热更（修改后约 3s 自动生效）。

### 底座配置

| 项 | 默认值 | 说明 |
|----|--------|------|
| `--listen` | `:8080` | 代理监听地址 |
| `--upstream` | `http://127.0.0.1:8080` | 默认后端 |
| `--admin` | `127.0.0.1:19527` | 管理接口（回环，不对外网） |
| `--timeout` | `5s` | 转发超时 |
| `--config` | 空 | `.env` 配置文件路径 |

### 启用挂件（热开关，不重启）

```bash
# 查看挂件状态
curl http://127.0.0.1:19527/admin/switch/list

# 启用 L1 防护
curl -X POST http://127.0.0.1:19527/admin/switch/on/shield

# 关闭 L2 路由（请求回退默认后端）
curl -X POST http://127.0.0.1:19527/admin/switch/off/dispatch
```

每个挂件的配置项均通过环境变量 / `.env` 设置，热更生效（详见开发手册 `docs/COMPONENTS.md`）。

# 管理控制台（WebUI）

浏览器打开 `http://127.0.0.1:19527/` 即得图形化管理控制台（纯静态单页，内嵌在二进制中，零额外部署）：

- **概览**：网关状态、运行指标、降级链可视化、组件总览
- **组件**：13 个挂件启停与配置（二次确认，失败原因透出）
- **配置**：全部配置项分组查看与热改（即时生效，无需重启）
- **脚本**：RockScript 策略发布与版本回滚
- **观测**：指标趋势图 + 按天访问日志查询

产品设计见 `docs/webui.md`，对接契约见 `docs/webui-api.md`。控制台仅监听回环地址，勿对外暴露。

---

## 部署

1. **单二进制分发**：纯 Go 无 CGO，编译产物为单个可执行文件，零外部依赖。
2. **多副本**：无状态转发层，可多副本水平扩展；配置集中下发（RockConfig / 环境变量）。
3. **优雅停机**：`Ctrl+C` 触发排空，在途请求不丢失（30s 超时）。
4. **观测**：启用 obs 后，访问日志落在 `logs/access-YYYY-MM-DD.jsonl`（按天切分、超期清理），指标查询 `GET /admin/metrics`。
5. **业务层**：stbiz_* 微服务（Python/PHP）不依赖 SDK 也可被代理（裸转发）；如需内网互调，使用 `sdk/python`（HTTP 一等公民，gRPC 可选）。

---

## 使用注意事项

- **WAF / 限流 / 路由等全部默认关闭**：需要防护与分发时显式开启对应挂件；关闭即降级为裸代理，转发不中断。
- **WebSocket 不支持**：Upgrade 请求返回 501（架构红线：只转发 HTTP 请求/响应）。
- **大文件上传/下载不中转**：避免二进制流占用代理。
- **管理接口仅监听回环地址**（默认 `127.0.0.1:19527`），勿对外网暴露。
- **WAF 规则外置目录**（默认 `rules/`）：改规则无需重新编译，`SHIELD_RULES_DIR` 指定，缺失回退内嵌规则。
- **SQL 脚本外置目录**（默认 `sql/`）：数据访问层脚本优先加载外置目录，改 SQL 无需重新编译。
- **数据库零配置**：默认 SQLite 本地文件 `rocksys.db`，可经 `DB_DRIVER` / `DB_DSN` 切换 MySQL / PostgreSQL（缺方言脚本即报错）。

---

## 架构概览

```
请求 ─▶ [L1 防护] ─▶ [L2 分发] ─▶ [L3 结果] ─▶ 后端
        （可选）      （可选）      （可选）

转发链（chain）：Head → Middle → [转发] → Tail(响应处理)
三层时间戳：begin_at → begin_biz_at → done_biz_at → 响应
```

三层时间戳是转发链的固定测量点：

```
耗时分解：防护 = begin_biz_at − begin_at
          业务 = done_biz_at − begin_biz_at
          总   = done_biz_at − begin_at
```

## 文档

| 文档 | 面向 | 内容 |
|------|------|------|
| [README.md](README.md) | 终端用户 | 本页：功能、构建、部署、使用 |
| [docs/COMPONENTS.md](docs/COMPONENTS.md) | 开发者 | 各组件/子组件作用与使用方法、配置项详解 |
| [docs/webui.md](docs/webui.md) | 产品 | 管理控制台产品设计（页面/交互/视觉规范） |
| [docs/webui-api.md](docs/webui-api.md) | 前端 | 管理接口契约（WebUI 对接唯一权威，无需读源码） |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构 | 设计底座：转发链、三层时间戳、降级链、红线 |
| [docs/PROJECT_STRUCTURE.md](docs/PROJECT_STRUCTURE.md) | 开发者 | 目录结构、模块关系、热运维引擎 |
| [docs/DEV_HANDBOOK.md](docs/DEV_HANDBOOK.md) | AI/实现 | 详细技术规格，供对照实现 |
| [docs/WORK_PROGRESS.md](docs/WORK_PROGRESS.md) | 维护 | 工作进度与批次日志 |

---

## 技术栈

- Go 1.24+，纯 Go 无 CGO
- 依赖地基库：`easyserver`（HTTP 服务器框架）、`easyconf`（配置）、`easydb`（数据访问）
- 独立子仓库，主模块经 `go.mod replace` 引用

## 许可证

[Apache-2.0](LICENSE)（如无 LICENSE 文件请以仓库为准）
