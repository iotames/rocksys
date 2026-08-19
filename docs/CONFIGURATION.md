# 配置

> 全部配置项、`bin/.env` 示例与管理接口令牌说明。由 README.md「配置」章节下沉而来。

## 配置来源

所有配置支持三种来源（优先级从高到低）：**命令行参数 > 环境变量 > 工作目录 `.env` 配置文件**（开发规范下即 `bin/.env`）。配置文件热更（修改后约 3s 自动生效，无需重启）。**第一原则「热更即持久化」**：运行时热改（`PUT /admin/config`、WebUI「配置」页、RockConfig）一律立即生效并同步写回配置文件，重启后保留。

> **配置中心红线**：禁止在项目根目录运行程序（会在根目录残留运行时文件）。运行时文件跟随**工作目录**生成；开发规范必须在 `bin/` 目录运行（`make run`/`make gen-env` 已 `cd bin`），故实际落点为 `bin/.env`、`bin/default.env`。`bin/default.env` 为全量默认值快照（装配完成自动同步，`make gen-env` 主动刷新），参与运行期取值（最低优先级兜底，优先级由 easyconf 决定）。

## 底座配置

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
| `HOT_SCRIPTS_DIR` | `hotscripts` | 脚本外挂统一根目录：各挂件子目录固定（`sql/`、`rules/`、`trusted_proxies/`），外挂优先、内嵌兜底；SQL/WAF 规则/可信代理改文件无需重新编译 |
| `TRUSTED_PROXIES_FILE` | `trusted_proxies.txt` | 可信代理列表文件（相对 `HOT_SCRIPTS_DIR/trusted_proxies/` 外置目录，不允许绝对路径；外挂优先，缺失回退内嵌默认 `127.0.0.1`；启动加载一次，改外挂文件需重启） |

## 可信代理模型

获取客户端真实 IP（访问日志/WAF/限流/哈希/转发链路统一经 `netutil.GetClientIP`）——直连源 IP（TCP 层）命中可信代理列表时才信任 `X-Real-IP` / `X-Forwarded-For` 转发头，否则直接返回直连源 IP，防公网直连伪造。转发头解析：`X-Real-IP`（覆写语义，校验合法即用）→ `X-Forwarded-For` 从右往左跳过可信代理取第一个合法 IP → 兜底直连源 IP。列表每行一个 IP 或 CIDR 网段（如 `10.0.0.0/8`），`#` 注释、空行忽略；外挂文件放**工作目录 `hotscripts/trusted_proxies/`** 下（开发规范下即 `bin/hotscripts/trusted_proxies/`，如 `bin/hotscripts/trusted_proxies/trusted_proxies.txt`）。默认仅 `127.0.0.1`；IPv6 本机 Nginx（`::1`）等场景需在外挂文件显式加入。

## 配置文件示例（`bin/.env`）

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

# ===== 网络层：可信代理（客户端真实 IP 获取）=====
# 相对 HOT_SCRIPTS_DIR/trusted_proxies/ 外置目录的文件路径（不允许绝对路径）；外挂优先，缺失回退内嵌默认 127.0.0.1；启动加载一次
TRUSTED_PROXIES_FILE = trusted_proxies.txt

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

# ===== 脚本外挂统一入口 =====
# 各挂件外挂子目录固定：sql/（数据访问层 SQL）、rules/（WAF 规则）、trusted_proxies/（可信代理）
# 外挂优先、内嵌兜底；改文件无需重新编译
HOT_SCRIPTS_DIR = hotscripts

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
# SQL 方言脚本外挂目录固定为 HOT_SCRIPTS_DIR/sql/（见上方脚本外挂统一入口）
```

> 每个挂件默认关闭；`bin/.env` 里写配置不等于启用，需在 WebUI「组件」页或 `rockctl switch on` 显式开启。
> 全部挂件配置项详解见 [COMPONENTS.md](COMPONENTS.md)；完整键清单可在 WebUI「配置」页查看。

## 管理接口令牌

```bash
export ROCKSYS_ADMIN_TOKEN=your-secret
./bin/rocksys --upstream http://127.0.0.1:9000
# 静态令牌仅供管理接口绑定非回环地址（远程部署）时使用：curl / rockctl 请求需带请求头 Authorization: Bearer your-secret；
# 回环地址本机访问免鉴权
```

> `ROCKSYS_ADMIN_TOKEN` 经配置中心注册（同 `bin/.env`/环境变量/命令行取值链），热更立即生效；也可在 WebUI「配置」页查看/修改；仅非回环部署生效，令牌无过期与轮换机制，配置者须自行定期轮换。
