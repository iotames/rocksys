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

| 挂件 | 作用 | 挂载 | 父开关 | 子开关 / 内部子组件 |
|------|------|------|--------|---------------------|
| **shield** | L1 防护：IP 黑白名单、路径/UA 规则、令牌桶限流、WAF（SQL/XSS/路径遍历/风险路径/爬虫 UA，规则外置文件可热改） | Head | `SHIELD_ENABLED` | 子开关：WAF 五项检测 `SHIELD_WAF_SQL_INJECTION` / `_XSS` / `_PATH_TRAVERSAL` / `_RISK_PATH` / `_CRAWLER_UA`；拦截事件落库 `SHIELD_EVENT_LOG_ENABLED` / `SHIELD_EVENT_PRUNE_ENABLED` |
| **trace** | trace_id 透传 | Head | `TRACE_ENABLED` | — |
| **auth** | JWT 认证 | Head | `AUTH_ENABLED` | — |
| **dispatch** | L2 路由分发：Radix Tree 前缀树（支持参数 `:id`、通配 `*`、最长匹配）、节点组、平滑加权轮询 / 一致性哈希、主动健康检查、高优/备份节点 | Middle | `DISPATCH_ENABLED` | 内部子组件：Radix Tree 路由引擎（router.go）、平滑加权轮询（balancer.go）、一致性哈希（chash.go）、主动健康检查（healthcheck.go） |
| **rewrite** | 转发前改写 URI / 请求头 | Middle | `REWRITE_ENABLED` | — |
| **script** | RockScript：Lua 策略引擎（网关策略，不承载业务逻辑） | Middle | `SCRIPT_ENABLED` | — |
| **obs** | RockObs：访问日志（异步落盘）+ 指标聚合 + 查询 API | Tail | `OBS_ENABLED` | 子开关：access_log 自动清理 `OBS_LOG_PRUNE_ENABLED`；存储后端 `OBS_STORE`（`db`\|`file`） |
| **copy** | 请求抄送：复制流量异步发送到 shadow 后端（审计/影子验证） | Tail | `COPY_ENABLED` | `COPY_TARGETS` 为空即不发送 |
| **result** | L3 结果处理：响应封装 / 字段脱敏 | Tail | `RESULT_ENABLED` | 功能项：响应封装 `RESULT_WRAP`、字段脱敏 `RESULT_MASK_FIELDS` |
| **config** | RockConfig：配置热更下发 | 独立组件 | 无（无条件注册） | — |
| **registry** | RockRegistry：服务注册与发现 | 独立组件 | 无（无条件注册） | — |
| **object** | RockObject：对象存储（S3 兼容） | 独立组件 | 无（无条件注册） | — |
| **mq** | RockMQ：异步消息（Outbox 模式） | 独立组件 | `MQ_ENABLED`（条件装配） | 内部：outbox 表建于统一数据访问层业务库，与业务数据同库 |

> 父子开关语义：父开关（`XXX_ENABLED`）关闭 = 挂件不挂载，其下所有子开关一律不生效；父开关开启后，各子开关按自身值决定对应子功能是否启动。配置项详解见 [docs/COMPONENTS.md](docs/COMPONENTS.md)。

### 降级链（高可用的真正含义）

```
全量能力 ─▶ 关L3 ─▶ 关L2 ─▶ 关L1 ─▶ 裸反向代理（永远可达）
```

任何一环故障 → 热关闭 → 请求绕过该环直通下一级。转发行为永不中断。

---

## 快速开始

完整上手指引（构建 / 开发模式免编译改前端 / 运行 / 打开 WebUI / 验证代理）见 **[docs/QUICKSTART.md](docs/QUICKSTART.md)**。

编译 - 运行 - 浏览器打开 `http://127.0.0.1:19527/`

```bash
# 编译
go build -o bin/rocksys ./cmd/rocksys

# 运行
# 显式指定三个地址（最清晰）：
#   --listen    :8080                  代理对外端口（客户端访问 http://<host>:8080/）
#   --upstream  http://127.0.0.1:9000  被代理的后端（业务服务）
#   --admin     127.0.0.1:19527        管理/WebUI 地址（浏览器打开 http://127.0.0.1:19527/）
cd bin && ./rocksys --listen :8080 --upstream http://127.0.0.1:9000 --admin 127.0.0.1:19527
```

---

## 管理控制台（WebUI）使用指南

> 纯静态单页，随二进制分发，无需单独安装。产品设计见 `docs/webui.md`，对接契约见 `docs/webui-api.md`。

| 页面 | 做什么 |
|------|--------|
| **概览** | 巡检入口：网关信息、实时指标（QPS/延迟分位/错误率，含趋势图）、**HTTP 数据流图**（组件节点带开关）、服务状态总览（卡片带开关） |
| **组件** | 9 个数据流组件，一组件一详情页（状态/配置双页签）；一键开启/关闭（二次确认，失败原因透出） |
| **服务** | 4 个独立服务（配置服务/注册/存储/消息）同款详情页 |
| **全局配置** | 网关 / 数据访问 / 其他基础设施配置；组件与服务配置前往各自详情页；行内编辑**即时生效、无需重启**；敏感项默认掩码；支持恢复默认值 |
| **脚本** | 编写/发布 RockScript 策略（Lua，带语法着色与校验）；版本时间线一键回滚或移除 |
| **观测 · WAF安全防护** | WAF 拦截统计（实时计数/按日趋势/Top 攻击源/明细）与黑白名单管理 |
| **观测 · 入网数据** | 按日期范围查询 HTTP 入网请求日志；按请求标识（trace_id）过滤、只看异常；行展开查看分环节耗时与转发目标 |
| **观测 · 系统日志** | 系统进程实时日志（ring buffer 实时监控） |

**通用交互**：
- 顶栏可设「自动刷新」（关/5s/15s/30s，作用于概览/组件与服务的状态页签/WAF安全防护；配置页签编辑中不打断）；全局配置/脚本/日志手动刷新。
- 所有写操作（启停组件/改配/发布脚本）均为：**二次确认 → 执行 → 结果提示 → 自动刷新**。
- 网关不可达时显示「管理接口不可达」横幅并保留上次数据。

**WebUI 专属说明**：
- 脚本为内存态：**网关重启后需重新发布**。
- 控制台仅监听回环地址，勿对外暴露；如需远程访问，用 SSH 隧道或置于受控内网。

---

## 配置

配置来源（优先级从高到低）：**命令行参数 > 环境变量 > 工作目录 `.env` 配置文件**（开发规范下即 `bin/.env`），配置文件热更（修改后约 3s 生效，无需重启）；运行时热改一律立即生效并写回配置文件（「热更即持久化」）。

全部配置项、可信代理模型、`bin/.env` 完整示例与管理接口令牌见 **[docs/CONFIGURATION.md](docs/CONFIGURATION.md)**。

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

目录规划、systemd 服务、安全建议、升级与优雅重启、多副本、日志与留存见 **[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**。

核心要点：管理接口仅监听回环、严禁暴露外网（远程用 SSH 隧道）；升级替换二进制 + `systemctl restart` 即完成（SIGTERM 排空，在途请求不丢失）；转发层无状态，可多副本水平扩展。

### Nginx 反代部署（HTTPS / FastCGI 场景）

rockSys 是纯 HTTP 反代（**不支持 HTTPS 监听、不支持 FastCGI**），公网 HTTPS 由 Nginx 终结，rockSys 作中间防护层；Nginx 转发必须用 `proxy_pass`（`fastcgi_pass` 的 FastCGI 报文 rockSys 无法解析；php-fpm 后端由 Nginx 在 rockSys 之后继续 fastcgi 转发）。

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;   # rockSys 入口
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;                     # 客户端真实 IP
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; # 代理链路
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

**数据库 `client_ip` 取值（可信代理模型）**：TCP 直连源 IP（`RemoteAddr`）未命中可信代理列表 → 直接用，不信任转发头；命中 → 依次取 `X-Real-IP`（合法即用）→ `X-Forwarded-For`（从右往左跳过可信代理，取第一个合法且不可信 IP）→ 兜底直连 IP。

**默认可信代理仅 `127.0.0.1`**（内嵌兜底；外挂文件 `bin/hotscripts/trusted_proxies/trusted_proxies.txt` 优先，每行一个 IP 或 CIDR，改后 ≤3s 热更）。Nginx 与 rockSys 同机（经 127.0.0.1 转发）无需配置；Nginx 在容器/他机时须把其 IP 加入该文件，否则取到的是 Nginx 的 IP：

```
# Nginx 在 docker 容器时，加入容器 IP 或网段
172.18.0.2
# 或整个网段：172.18.0.0/16
```

**rockSys 启动**：`cd bin && ./rocksys --listen 127.0.0.1:8080 --upstream http://<后端HTTP服务> --admin 127.0.0.1:19527`

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
- **WAF 规则外置目录**（`HOT_SCRIPTS_DIR/rules/`，默认 `hotscripts/rules/`）：改规则无需重新编译，外挂优先、缺失回退内嵌规则。
- **SQL 脚本外置目录**（`HOT_SCRIPTS_DIR/sql/`，默认 `hotscripts/sql/`）：数据访问层脚本优先加载外置目录，改 SQL 无需重新编译。
- **数据库零配置**：默认 SQLite 本地文件 `rocksys.db`，可经 `DB_DRIVER` / `DB_DSN` 切换 MySQL / PostgreSQL（缺方言脚本即报错）。

### 数据库铁律

1. **SQL 落盘**：所有数据库操作写成独立 `.sql` 文件，放 `sql/<dbtype>/`（`sql/sqlite/`、`sql/mysql/`、`sql/postgres/`），禁止 Go 代码内联 SQL。
2. **换库只改 bin/.env**：切换数据库仅改 `DB_DRIVER` / `DB_DSN`（SQL 方言脚本外挂目录固定 `HOT_SCRIPTS_DIR/sql/`），不改代码、不重编译。
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
