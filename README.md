# RockSys 磐石系统

**极简增强式反向代理底座**：默认一台 HTTP 反向代理（等同极简 NGINX `proxy_pass`），能力按需挂载、全部可热开关。任何组件掉链子，紧急关闭，转发依旧。

> 核心哲学：**只有转发是必须的**。其余一切皆是可选增强，默认全关，可随时热插拔。

---

## 产品功能

### 底座（必需，不可关）

| 能力 | 说明 |
|------|------|
| 反向代理引擎 | 接收全部 HTTP 请求 → 转发 → 回传响应，协议级纯转发 |
| 转发超时 | 慢后端不挂死代理连接（默认 18s，可配置） |
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

### 1. 构建

**环境要求**：Go 1.25+（Linux / macOS / Windows 均可，产物为 Linux 目标时可直接交叉编译）。

```bash
# 同步依赖地基库（easyconf/easyserver/easydb）并构建
make build                        # 产物 bin/rocksys

# 查看版本（版本号 = 当前 git 最新 tag）
./bin/rocksys --version

# 或跳过 make deps，直接构建（不注入版本，--version 显示 dev）
go build -o bin/rocksys ./cmd/rocksys

# 交叉编译生产产物（纯 Go 无 CGO，可直接产出目标平台二进制）
# 产物 bin/rocksys-linux-amd64、bin/rocksys-linux-arm64
make cross-build
```

> 构建产物为**单个可执行文件**，WebUI 管理控制台已 `go:embed` 内嵌在二进制中，零额外前端文件。

### 2. 运行

运行 RockSys 会同时出现 **3 个地址**，先看清各自职责：

```
客户端 ──HTTP──▶ 代理监听端口  ──转发──▶ 被代理的后端（upstream）
                 （默认 :8080）            （默认 http://127.0.0.1:8080）
                      │
                      └── 管理接口 + WebUI 控制台（默认 127.0.0.1:19527，仅回环）
```

| 地址 | 默认值 | 是谁 | 谁访问 |
|------|--------|------|--------|
| 代理监听端口 | `:8080` | 对外收请求的入口 | 客户端/浏览器 |
| 被代理后端 | `http://127.0.0.1:8080` | 真正干活的业务服务 | 只有代理转发给它 |
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

### 3. 打开管理控制台（WebUI）

浏览器访问 **`http://127.0.0.1:19527/`**（即 `--admin` 指定的地址）即打开图形化管理控制台（默认仅监听回环地址）。

首次使用：
1. 默认监听回环地址，本机访问免登录。
2. 默认落地「概览」页：查看网关状态、运行指标、降级链与组件总览。

### 4. 验证代理

```bash
# 8080 是代理端口；此请求经代理转发到后端（上例 :9000），返回后端响应原样
curl http://127.0.0.1:8080/hello
# → 上游响应原样返回
```

---

## 管理控制台（WebUI）使用指南

> 纯静态单页，随二进制分发，无需单独安装。产品设计见 `docs/webui.md`，对接契约见 `docs/webui-api.md`。

| 页面 | 做什么 |
|------|--------|
| **概览** | 巡检入口：网关信息、实时指标（QPS/延迟分位/错误率）、**降级链可视化**、13 个组件状态总览 |
| **组件** | 查看全部挂件启停状态；一键开启/关闭（二次确认，失败原因透出）；展开卡片查看运行信息与配置 |
| **配置** | 全部配置项按「网关 + 各组件」分组展示；行内编辑**即时生效、无需重启**；敏感项默认掩码；支持恢复默认值 |
| **脚本** | 编写/发布 RockScript 策略（Lua，带语法着色与校验）；版本时间线一键回滚或移除 |
| **观测 · 指标** | 实时指标卡 + 趋势折线图（按刷新周期累积采样） |
| **观测 · 日志** | 按日期范围查询访问日志；按请求标识（trace_id）过滤、只看异常；行展开查看分环节耗时与转发目标 |

**通用交互**：
- 顶栏可设「自动刷新」（关/5s/15s/30s，作用于概览/组件/指标）；配置/脚本/日志手动刷新。
- 所有写操作（启停组件/改配/发布脚本）均为：**二次确认 → 执行 → 结果提示 → 自动刷新**。
- 网关不可达时显示「管理接口不可达」横幅并保留上次数据。

**WebUI 专属说明**：
- 脚本为内存态：**网关重启后需重新发布**。
- 控制台仅监听回环地址，勿对外暴露；如需远程访问，用 SSH 隧道或置于受控内网。

---

## 配置

所有配置支持三种来源（优先级从高到低）：**命令行参数 > 环境变量 > 工作目录 `.env` 配置文件**（开发规范下即 `bin/.env`）。配置文件热更（修改后约 3s 自动生效，无需重启）。**第一原则「热更即持久化」**：运行时热改（`PUT /admin/config`、WebUI「配置」页、RockConfig）一律立即生效并同步写回配置文件，重启后保留。

> **配置中心红线**：禁止在项目根目录运行程序（会在根目录残留运行时文件）。运行时文件跟随**工作目录**生成；开发规范必须在 `bin/` 目录运行（`make run`/`make gen-env` 已 `cd bin`），故实际落点为 `bin/.env`、`bin/default.env`。`bin/default.env` 为全量默认值快照（装配完成自动同步，`make gen-env` 主动刷新），参与运行期取值（最低优先级兜底，优先级由 easyconf 决定）。

### 底座配置

| 项 | 默认值 | 说明 |
|----|--------|------|
| `--listen` / `ROCKSYS_LISTEN` | `:8080` | 代理监听地址 |
| `--upstream` / `ROCKSYS_UPSTREAM` | `http://127.0.0.1:8080` | 默认后端 |
| `--admin` / `ROCKSYS_ADMIN` | `127.0.0.1:19527` | 管理接口（回环，不对外网） |
| `--timeout` / `ROCKSYS_TIMEOUT` | `18` | 转发超时（秒） |
| `--config` / `ROCKSYS_CONFIG` | 空 | `bin/.env` 配置文件路径（任意位置） |
| `ROCKSYS_LOG_LEVEL` | `info` | 日志级别（debug/info/warn/error） |
| `ROCKSYS_LOG_TO_FILE` | `false` | 日志文件存档开关 |
| `ROCKSYS_LOG_FILE` | `logs/rocksys.log` | 日志文件路径（相对工作目录） |
| `ROCKSYS_LOG_MAX_SIZE` | `50` | 日志文件大小上限（整数 MB，0=不限制） |

### 配置文件示例（`bin/.env`）

```bash
# ===== 底座 =====
ROCKSYS_LISTEN = :8080
ROCKSYS_UPSTREAM = http://127.0.0.1:9000
ROCKSYS_TIMEOUT = 18
ROCKSYS_ADMIN = 127.0.0.1:19527
ROCKSYS_LOG_LEVEL = info
ROCKSYS_LOG_TO_FILE = false
ROCKSYS_LOG_FILE = logs/rocksys.log
ROCKSYS_LOG_MAX_SIZE = 50

# ===== 防护 shield（L1）=====
SHIELD_ENABLED = true
SHIELD_IP_BLACKLIST = 10.0.0.5,192.168.1.0/24
SHIELD_IP_WHITELIST =
SHIELD_RATE_LIMIT_RPS = 100
SHIELD_RATE_LIMIT_BURST = 50
SHIELD_ALLOW_METHODS = GET,POST,PUT,DELETE
SHIELD_MAX_BODY_SIZE = 10485760
SHIELD_WAF_SQL_INJECTION = true
SHIELD_WAF_XSS = true
SHIELD_WAF_PATH_TRAVERSAL = true
SHIELD_WAF_RISK_PATH = true
SHIELD_WAF_CRAWLER_UA = true
SHIELD_RULES_DIR = rules

# ===== 分发 dispatch（L2）=====
# 格式：<prefix>=<spec>[;<spec>...]；节点 <url>[|w=权重]；可选 @间隔@超时@路径 健康检查
# 例：/api/order/ 走 o1/o2 两个节点（加权轮询 + 健康检查）
DISPATCH_RULES = /api/order/=http://o1:9001;http://o2:9001|w=2@10s@2s@/healthz;/=http://default-svc:9000

# ===== 改写 rewrite =====
REWRITE_RULES = /api/v1/=uri|/api/;header=X-Proxy-Tag:rewrite

# ===== 观测 obs =====
OBS_STORE = db               # 访问日志存储后端（默认）：db（数据库，复用 DB_DRIVER/DB_DSN）| file（JSONL，已弃用，将不再被支持）
OBS_LOG_DIR = logs           # 仅 file 后端使用（遗留）
OBS_RETENTION_DAYS = 30

# ===== 抄送 copy =====
COPY_TARGETS =

# ===== 结果 result（L3）=====
RESULT_WRAP =
RESULT_MASK_FIELDS = phone,id_card

# ===== 认证 auth =====
AUTH_ENABLED = true
AUTH_JWT_SECRET = change-me
AUTH_JWT_ISSUER = rocksys
AUTH_JWT_TTL = 3600

# ===== 脚本 script（Lua 策略）=====
SCRIPT_TIMEOUT = 100            # Lua 脚本执行超时（毫秒）

# ===== 消息 mq（outbox 表建于下方数据访问层业务库，DB_DRIVER/DB_DSN）=====
MQ_ENABLED = false
MQ_POLL_INTERVAL = 1000         # 轮询间隔（毫秒）
MQ_MAX_RETRIES = 3              # 最大重试次数（超限转死信；0 视为未设置，回落默认 3）
MQ_BASE_BACKOFF = 100           # 指数退避基数（毫秒）
MQ_CONSUMER_BASE_URL =          # 默认消费方地址（未命中 topic 路由时使用）

# ===== 注册中心 registry =====
REGISTRY_ADDR = :9800           # 注册服务监听地址
REGISTRY_TTL = 30               # 心跳超时（秒）
REGISTRY_STATIC_FILE =          # 静态实例文件路径（YAML/JSON，空=无静态实例）

# ===== 对象存储 object =====
OBJECT_BASE_DIR = ./data/object

# ===== 数据访问层 =====
DB_DRIVER = sqlite
DB_DSN = rocksys.db               # 默认已含 ?_busy_timeout=5000&_journal_mode=WAL，可显式覆盖；mysql/postgres 示例见注释
SQL_DIR = sql
```

> 每个挂件默认关闭；`bin/.env` 里写配置不等于启用，需在 WebUI「组件」页或 `rockctl switch on` 显式开启。
> 全部挂件配置项详解见 `docs/COMPONENTS.md`；完整键清单可在 WebUI「配置」页查看。

### 管理接口令牌

```bash
export ROCKSYS_ADMIN_TOKEN=your-secret
./bin/rocksys --upstream http://127.0.0.1:9000
# 静态令牌仅供管理接口绑定非回环地址（远程部署）时使用：curl / rockctl 请求需带请求头 Authorization: Bearer your-secret；
# 回环地址本机访问免鉴权
```

> `ROCKSYS_ADMIN_TOKEN` 经配置中心注册（同 `bin/.env`/环境变量/命令行取值链），热更立即生效；也可在 WebUI「配置」页查看/修改；仅非回环部署生效，令牌无过期与轮换机制，配置者须自行定期轮换。

---

## 常用运维命令

### rockctl（命令行工具）

`rockctl` 是独立的运维 CLI（与 `rocksys` 是两个二进制）：

```bash
# 构建 rockctl
go build -o bin/rockctl ./cmd/rockctl

# 默认连 127.0.0.1:19527；远程可 --admin 指定；令牌经环境变量 ROCKSYS_ADMIN_TOKEN（仅管理接口绑定非回环地址时使用）
rockctl switch list                # 列出组件状态
rockctl switch on shield           # 开启防护
rockctl switch off dispatch        # 关闭路由（回退默认后端）
rockctl config get                 # 查看当前配置
rockctl config set ROCKSYS_UPSTREAM http://127.0.0.1:9001   # 热改配置
rockctl script publish rule.lua    # 发布 Lua 策略
rockctl script rollback            # 回滚脚本
```

### 直接调用管理 API

```bash
curl http://127.0.0.1:19527/admin/switch/list
curl -X POST http://127.0.0.1:19527/admin/switch/on -d '{"name":"shield"}'
curl -X PUT  http://127.0.0.1:19527/admin/config -d '{"ROCKSYS_UPSTREAM":"http://127.0.0.1:9001"}'
curl http://127.0.0.1:19527/admin/metrics
curl "http://127.0.0.1:19527/admin/logs?from=2026-08-04&to=2026-08-04"
```

---

## 生产部署

### 目录规划

```text
/opt/rocksys/
├── bin/rocksys              # 编译产物（或 cross-build 产物）
├── rocksys.env              # 配置文件
├── logs/                    # 访问日志（obs 启用后，按天切分）
├── rules/                   # WAF 规则外置目录（可选）
├── sql/                     # SQL 脚本外置目录（可选）
└── rocksys.db               # 默认 SQLite 数据库（自动创建）
```

### systemd 服务

```ini
# /etc/systemd/system/rocksys.service
[Unit]
Description=RockSys Gateway
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/rocksys
ExecStart=/opt/rocksys/bin/rocksys --config /opt/rocksys/rocksys.env
Restart=always
RestartSec=3
# 管理接口令牌（可选）
Environment=ROCKSYS_ADMIN_TOKEN=change-me

[Install]
WantedBy=multi-user.target
```

```bash
sudo cp bin/rocksys /opt/rocksys/bin/
sudo systemctl daemon-reload
sudo systemctl enable --now rocksys
```

### 安全建议

1. **管理接口仅监听回环**（默认 `127.0.0.1:19527`），严禁 `ROCKSYS_ADMIN` 设为 `0.0.0.0` 暴露外网。
2. 远程管理用 SSH 隧道：`ssh -L 19527:127.0.0.1:19527 user@host` 后浏览器访问 `http://127.0.0.1:19527/`。
3. 回环地址本机免鉴权；静态 token（`ROCKSYS_ADMIN_TOKEN`）仅用于非回环部署的远程调用鉴权。
4. 对外暴露的监听端口建议置于防火墙 / 安全组之后。

### 升级与优雅重启

```bash
# 1. 替换二进制
sudo cp bin/rocksys /opt/rocksys/bin/rocksys
# 2. 优雅重启（SIGTERM 触发排空，在途请求不丢失，30s 超时）
sudo systemctl restart rocksys
```

配置与前端均内嵌/外置，升级二进制即可，无需迁移数据（状态为内存态，日志落盘保留）。

### 多副本

转发层无状态，可多副本水平扩展：

```bash
./bin/rocksys --listen :8081 --config rocksys.env &
./bin/rocksys --listen :8082 --config rocksys.env &
```

前方用负载均衡器 / DNS 轮询分发；配置集中下发（同一配置文件或环境变量）。

### 日志与留存

- obs 启用后：访问日志默认写入 `access_log` 表（`OBS_STORE=db`，复用 `DB_DRIVER`/`DB_DSN`；数据库不可用时回退 JSONL 文件并告警）。`OBS_STORE=file` 已弃用，将不再被支持。WebUI「日志」页支持按时间范围（精确到分）+ 路径精确/模糊过滤查询。
- 指标：`GET /admin/metrics`（1 分钟滑动窗口），WebUI「观测 · 指标」查看趋势。
- 业务日志与网关日志分离；如需聚合到统一平台，可对接日志采集器消费 `logs/` 目录。

---

## 故障与降级

| 症状 | 处理 |
|------|------|
| 后端全挂 | dispatch 健康检查将节点摘除；全挂写 503 中断链；关闭 dispatch 即回退默认后端 |
| 防护误拦 | WebUI「组件」页关闭 shield（或调整规则）；转发自动降级不受影响 |
| Lua 脚本出错 | 自动回滚脚本或 `script rollback` 移除；脚本仅策略、不影响转发 |
| 配置改坏 | WebUI「配置」页恢复默认值，或改回 `bin/.env` 后 3s 热更生效 |
| 组件故障 | 关闭该组件即摘除环节，转发链自动降级，**转发永不中断** |

---

## 使用注意事项

- **WAF / 限流 / 路由等全部默认关闭**：需要防护与分发时显式开启对应挂件；关闭即降级为裸代理，转发不中断。
- **WebSocket 支持**：Upgrade 握手（101）后进入双向字节隧道，ws 帧原样透传；握手前仍走完整中间件链（认证/限流/trace 照常生效），后端拒绝升级（非 101）按普通响应透传。
- **大文件上传/下载不中转**：避免二进制流占用代理。
- **管理接口仅监听回环地址**（默认 `127.0.0.1:19527`），勿对外网暴露。
- **WAF 规则外置目录**（默认 `rules/`）：改规则无需重新编译，`SHIELD_RULES_DIR` 指定，缺失回退内嵌规则。
- **SQL 脚本外置目录**（默认 `sql/`）：数据访问层脚本优先加载外置目录，改 SQL 无需重新编译。
- **数据库零配置**：默认 SQLite 本地文件 `rocksys.db`，可经 `DB_DRIVER` / `DB_DSN` 切换 MySQL / PostgreSQL（缺方言脚本即报错）。

### 数据库铁律

1. **SQL 落盘**：所有数据库操作写成独立 `.sql` 文件，放 `sql/<dbtype>/`（`sql/sqlite/`、`sql/mysql/`、`sql/postgres/`），禁止 Go 代码内联 SQL。
2. **换库只改 bin/.env**：切换数据库仅改 `DB_DRIVER` / `DB_DSN`（`SQL_DIR` 默认 `sql`），不改代码、不重编译。
3. **纯 SQL 原生**：不用对象模型 / ORM，参数化占位符 `?`（sqlite/mysql）或 `$1`（postgres）；动态标识符 `{xxx}` 禁止来自外部输入。
4. **方言齐平**：SQL 变更须同步 sqlite/mysql/postgres 三份方言脚本；缺脚本即运行时报错（`internal/db.SQL()` 强制校验），不悄悄降级。

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
| [docs/HTTP_DATAFLOW.md](docs/HTTP_DATAFLOW.md)|开发者/终端用户| 网络数据流转过程解析 |
| [docs/COMPONENTS.md](docs/COMPONENTS.md) | 开发者 | 各组件/子组件作用与使用方法、配置项详解 |
| [docs/webui.md](docs/webui.md) | 产品 | 管理控制台产品设计（页面/交互/视觉规范） |
| [docs/webui-api.md](docs/webui-api.md) | 前端 | 管理接口契约（WebUI 对接唯一权威，无需读源码） |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构 | 设计底座：转发链、三层时间戳、降级链、红线 |
| [docs/PROJECT_STRUCTURE.md](docs/PROJECT_STRUCTURE.md) | 开发者 | 目录结构、模块关系、热运维引擎 |
| [docs/DEV_HANDBOOK.md](docs/DEV_HANDBOOK.md) | AI/实现 | 详细技术规格，供对照实现 |

---

## 技术栈

- Go 1.25+，纯 Go 无 CGO
- 依赖地基库：`easyserver`（HTTP 服务器框架）、`easyconf`（配置）、`easydb`（数据访问）
- 独立子仓库，主模块经 `go.mod replace` 引用

## 许可证

[Apache-2.0](LICENSE)（如无 LICENSE 文件请以仓库为准）
