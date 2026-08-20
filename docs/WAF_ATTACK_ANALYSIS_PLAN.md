# WAF 防护增强方案：IP 黑白名单数据库化与管理面建设

> 版本：v1.0　|　状态：方案确认，待实施
> 读者：本方案的执行者（开发人员/AI 智能体）。本文档自包含，不依赖任何口头沟通上下文。
> 若对业务决策点有疑问，可查阅本文 §5（已确认决策）与 §8（未来方向），或向方案负责人提问。

---

## 1. 背景与需求

### 1.1 现状

RockSys 网关内置 WAF 防护（shield 组件，L1 链路）。当前防护清单：

| 能力 | 现状 |
|---|---|
| 爬虫/扫描器 UA 指纹拦截 | ✅ 已落地：`rules/crawler_ua.txt` 外挂文件（41 条指纹），`SHIELD_WAF_CRAWLER_UA=true` 开启后生效，空 UA 一并拦截 |
| IP 黑名单 | ✅ 已落地：`rules/ip_blacklist.txt` 外挂文件（已收录 403 条风险 IP），精确 IP/CIDR，≤3s 热更 |
| IP 白名单 | ✅ 已有 `.env` 配置 `SHIELD_IP_WHITELIST` |
| 拦截记录 | ✅ `shield_event` 表（block_type 1-10 拦截类别枚举） |

### 1.2 问题与需求

外挂文件形式的黑白名单存在明显局限：

1. **不可维护**：403 条黑名单只能靠手工编辑文件，无法按条件查询/分类/批量操作，无审计（谁在何时加了哪条）；
2. **无统计**：无法回答"黑名单里 SQL 注入类占多少""某 IP 被拦截了多少次"；
3. **无生命周期**：不支持临时封禁（过期自动失效）、软删除（误封恢复）；
4. **不可观测**：黑名单命中的计数只能看 `shield_event`，无法关联到具体黑名单条目。

**本方案目标**：将 IP 黑白名单升级为**数据库持久化 + 管理面（admin API + WebUI）+ 拦截链路查表**，
补齐上述能力，同时保留外挂文件作为静态兜底。

### 1.3 性能红线（最重要约束）

网关每秒处理大量请求，**任何一条请求的处理路径上都不允许发生数据库查询**。
黑白名单必须缓存进内存（不可变快照），请求只读快照；数据库仅在管理操作、启动加载、后台刷新时访问。
详见 §5.3。

---

## 2. 目标与非目标

### 2.1 本期目标（做）

- 新增三张数据表：`ip_blacklist`、`ip_whitelist`、`attack_archive`（三方言 SQL + 数据字典）；
- shield 拦截点接入数据库黑白名单（内存缓存，热路径零 DB 查询）；
- admin API：黑白名单 CRUD + 批量导入；WebUI 管理页；
- 存量迁移：现有外挂文件 403 条风险 IP 导入数据库。

### 2.2 非目标（本期不做，记入 §8 未来方向）

- 自动收录（高频请求自动拉黑、规则命中自动拉黑）；
- 429 限流式封禁；
- 攻击证据归档的业务逻辑（本期只建表）；
- 白名单 `.env` 配置迁移入表（本期两者并存）。

---

## 3. 总体架构

```
┌─ 数据层：DB 三表（ip_blacklist / ip_whitelist / attack_archive）── 持久化权威
├─ 管理层：admin API + WebUI ───────────────────────── 人工录入/查询/批量导入/软删
└─ 执行层：shield L1 拦截点 ─────────────────────────── 读内存快照 → 命中 403
```

**分层职责**：
- **数据层**：唯一持久化事实来源；三方言 SQL 脚本（`sql/{sqlite,postgres,mysql}/`）；
- **管理层**：运维人员操作入口；所有变更写库后**主动触发快照重建**；
- **执行层**：请求热路径**只读内存快照**，命中黑名单 → 403 + 拦截记录 + 命中计数（异步）。

---

## 4. 数据表设计

> ★ **数据字典红线**：三张表必须同步维护进 `docs/DATA_DICT.md`（字段标题/说明/默认值/三方言类型对照/枚举），
> 与既有 4 表（shield_event/access_log/admin_users/outbox）格式完全一致。
> 时间列统一：`DATETIME`（sqlite）/ `TIMESTAMPTZ`（postgres）/ `DATETIME(3)`（mysql，毫秒精度），存 UTC。

### 4.1 ip_blacklist — 动态 IP 黑名单表

| 字段 | sqlite | postgres | mysql | 约束 | 说明 |
|---|---|---|---|---|---|
| `id` | INTEGER PK AUTOINCREMENT | BIGSERIAL | BIGINT AUTO_INCREMENT | PK | 主键 |
| `ip` | TEXT | TEXT | VARCHAR(45) | UNIQUE NOT NULL | 精确 IP 或 CIDR（45 兼容 IPv6） |
| `title` | TEXT | TEXT | VARCHAR(64) | | 拉黑原因备注（如"Azure 云段扫描器"） |
| `block_type` | INTEGER | SMALLINT | SMALLINT | NOT NULL DEFAULT 1 | 拉黑原因类别：复用 `shield_event` 的 block_type 枚举（见 §4.4） |
| `hit_count` | INTEGER | INT | INT | NOT NULL DEFAULT 0 | 命中拦截计数（异步累加，观测/排序用） |
| `expires_at` | DATETIME | TIMESTAMPTZ | DATETIME(3) | | 过期时间；NULL=永久，过期条目不参与匹配 |
| `deleted_at` | DATETIME | TIMESTAMPTZ | DATETIME(3) | | 软删除时间；非 NULL 视为已删除，不参与匹配 |
| `created_at` | DATETIME | TIMESTAMPTZ | DATETIME(3) | NOT NULL | 创建时间 |
| `updated_at` | DATETIME | TIMESTAMPTZ | DATETIME(3) | NOT NULL | 最后更新时间 |

**索引**：
- `ip` 唯一索引（幂等：重复插入同一 IP 应被拒绝或 ON CONFLICT 忽略）；
- `block_type` 普通索引（按类别过滤/统计）；
- `expires_at` 普通索引（后台清理过期条目用）。

**写入语义**：人工录入时 `block_type` 必填（默认 1 通用拉黑）；批量导入默认 1，
导入后可按行为批量归类（如批量 SQL 探测→7、爬虫池→9、路径遍历→6）。

### 4.2 ip_whitelist — IP 白名单表

| 字段 | 类型（同 4.1 约定） | 约束 | 说明 |
|---|---|---|---|
| `id` | 同 | PK | 主键 |
| `ip` | 同 | UNIQUE NOT NULL | 精确 IP 或 CIDR |
| `title` | 同 | | 备注 |
| `deleted_at` | 同 | | 软删除 |
| `created_at` / `updated_at` | 同 | NOT NULL | 审计时间 |

**索引**：`ip` 唯一索引。

### 4.3 attack_archive — 攻击证据归档表（本期仅建表，不接业务逻辑）

| 字段 | 类型（同 4.1 约定） | 说明 |
|---|---|---|
| `id` | 同 | 主键 |
| `client_ip` | TEXT / TEXT / VARCHAR(45) | 来源 IP |
| `request_uri` | TEXT / TEXT / VARCHAR(1000) | 请求 URI（含查询串） |
| `request_headers` | TEXT / TEXT / TEXT | 完整请求头 JSON（攻击证据） |
| `block_type` | INTEGER / SMALLINT / SMALLINT | 拦截类别（复用 shield_event 枚举，见 §4.4） |
| `remark` | TEXT / TEXT / VARCHAR(64) | 归档备注 |
| `created_at` | 同 | 归档时间 |

**索引**：`client_ip`、`block_type`。数据**不自动清理**（审计留存）。

> 本期交付：建表脚本 + 数据字典登记 + 初始化建表；归档触发/查询逻辑留待后续迭代（§8）。

### 4.4 block_type 枚举（复用既有，勿重复定义）

本方案中 `block_type` **复用** `shield_event` 表既有枚举（权威定义见 `plugins/shield/block_type.go` 与 `docs/DATA_DICT.md`）：

| 值 | 含义 | 值 | 含义 |
|---|---|---|---|
| 1 | IP黑名单 | 6 | 路径遍历 |
| 2 | 限流 | 7 | SQL注入 |
| 3 | 方法不允许 | 8 | XSS |
| 4 | 请求体超限 | 9 | 爬虫UA |
| 5 | 风险路径 | 10 | 路径规则 |

> 枚举显示名以 `block_typeNames`（block_type.go）与 `docs/DATA_DICT.md` 为准，本表仅作速查。

**黑名单表中 `block_type` 的语义**：该 IP **因发起了哪类请求被识别封禁**（统计维度，非运行时匹配依据）。
运行时只按 `ip` 精确/CIDR 匹配；`block_type` 用于管理面过滤与统计。

---

## 5. 业务逻辑（已确认决策）

### 5.1 决策汇总

| 决策点 | 结论 |
|---|---|
| 拦截策略 | 命中黑名单一律 **403**（本期不做 429 限流式封禁） |
| 收录方式 | **仅人工**（管理面录入/批量导入）；不做自动收录 |
| 白名单共存 | `.env` 配置（`SHIELD_IP_WHITELIST`）∪ DB 表，**取并集** |
| 黑名单共存 | 外挂文件（`rules/ip_blacklist.txt`）∪ DB 表，**取并集**；迁移完成后外挂文件仅保留最小种子集作离线兜底，DB 表为唯一权威（见 B6） |
| 冲突语义 | **白名单优先**于黑名单（与现有行为一致） |
| 软删除/过期 | `deleted_at` 非 NULL 或 `expires_at` 已过期的条目**不参与匹配** |
| 命中计数 | 命中时**异步**累加 `hit_count`（不阻塞请求处理） |

### 5.2 拦截顺序（shield L1 Handle）

```
① 白名单匹配（.env ∪ DB 表快照）→ 命中直接放行（短路返回）
② 黑名单匹配（外挂文件 ∪ DB 表快照）→ 命中：写 403 响应 + shield_event 落库(block_type=1) + hit_count 异步+1
③ WAF 检测（爬虫 UA / 空 UA / SQL / XSS / 路径遍历 / 风险路径）
④ 路径/UA 规则 → 限流 → 转发
```

### 5.3 缓存机制（★ 性能红线，强制）

**目标**：请求热路径**零数据库查询**，性能与外挂文件模式一致（纯内存匹配）。

**实现要求**：
1. 黑白名单（DB 表 + 外挂文件 + .env 白名单）合并加载进**不可变快照**（复用 shield 现有快照机制）；
2. 快照重建触发：
   - 启动时加载；
   - **管理面任何变更（增删改/导入）成功后主动触发重建**；
   - **TTL 兜底刷新（默认 60s）**：后台定时器定期从 DB 刷新快照，覆盖"变更未通知"的异常场景；
3. 匹配逻辑：IP 精确匹配 + CIDR 包含（复用现有 `ipSet` 实现）；
4. 命中计数 `hit_count` 异步写入（批量/攒批，不阻塞、不每次请求写库）。

**验证要求（写进单测）**：注入 DB 访问计数器，断言"处理 N 条请求期间 DB 查询次数 = 0"。

### 5.4 管理面行为

- 新增/更新黑名单：必填 `ip`；`title` 建议必填；`block_type` 默认 1；`expires_at` 可选；
- 删除：**软删除**（`deleted_at = now`），管理面可恢复（清除 deleted_at）；
- 批量导入：接受"每行一个 IP/CIDR"格式（兼容现有外挂文件），重复 IP 幂等跳过；
- 列表：分页 + 按 `ip` 模糊 / `block_type` / 是否过期过滤；展示 `hit_count`。

---

## 6. 管理面（admin API + WebUI）

### 6.1 admin API（挂件 handler 范式，注册进 adminapi）

| 端点 | 方法 | 说明 |
|---|---|---|
| `/admin/shield/blacklist` | GET | 黑名单列表（分页/过滤：ip、block_type、过期状态） |
| `/admin/shield/blacklist` | POST | 新增（body: ip/title/block_type/expires_at） |
| `/admin/shield/blacklist/{id}` | PUT | 更新（title/block_type/expires_at） |
| `/admin/shield/blacklist/{id}` | DELETE | 软删除（`deleted_at = now`） |
| `/admin/shield/blacklist/{id}/restore` | POST | 恢复软删（清除 `deleted_at`） |
| `/admin/shield/whitelist/{id}/restore` | POST | 白名单同构恢复 |
| `/admin/shield/blacklist/import` | POST | 批量导入（body: 文本，每行一个 IP/CIDR） |
| `/admin/shield/whitelist` 及同上 | GET/POST/PUT/DELETE | 白名单同构操作（无 block_type/expires_at） |

约束：
- 方法校验（GET/POST 语义），错误响应不泄露内部细节；
- 鉴权走既有 admin 链路（回环免登录 / 登录 JWT）；
- 所有变更成功后**触发快照重建**（§5.3）。

### 6.2 WebUI

- 「拦截统计」页新增「黑白名单」管理 Tab：列表（含 hit_count/block_type/过期状态）、新增、软删/恢复、批量导入；
- 沿用现有前端模式（views/waf.js 同目录新增视图）。

---

## 7. 实施计划（执行者按序推进）

> 每步独立可交付；★ 为强制约束。每步完成跑 `go build`、`go vet`、相关单测。

### B1 三方言建表脚本 + 数据字典

- 交付：`sql/{sqlite,postgres,mysql}/` 下三个新文件：
  `ip_blacklist_create_table.sql`、`ip_whitelist_create_table.sql`、`attack_archive_create_table.sql`
- ★ 字段注释写全（标题/说明/默认值/枚举引用，沿用既有表注释风格，时间 UTC + DATETIME(3) 毫秒）
- ★ 同步 `docs/DATA_DICT.md`：新增 3 表章节（字段标题/说明/三方言类型对照/默认值），
  与既有 4 表格式完全一致；`block_type` 引用既有枚举章节，不重复定义
- ★ 同步外挂副本 `bin/hotscripts/sql/`（运行期优先读取）
- 验收：`go test ./internal/db/` 通过（三方言文件集一致性校验）；sqlite 建表可执行

### B2 数据访问层

- 交付：黑白名单表读写封装：
  - 查询全量有效条目（`deleted_at IS NULL` 且未过期）→ 供快照加载；
  - 单条 CRUD / 软删 / 恢复 / 批量导入（幂等）；
  - `hit_count` 异步累加
- ★ SQL 全部外置 `sql/<dbtype>/`（禁 Go 内联），参数化占位符
- 验收：表驱动单测覆盖 CRUD、软删/过期过滤、唯一约束幂等、批量导入去重

### B3 shield 拦截点接入

- 交付：快照新增黑白名单 ipSet（DB 表 ∪ 外挂文件 ∪ .env 白名单）；命中 403 + 落库 + hit_count 异步+1
- ★ 热路径零 DB 查询（仅读快照）；管理面变更主动重建 + TTL（60s）兜底刷新
- ★ 白名单优先短路语义保持
- 验收：单测覆盖 表黑名单命中 / 白名单优先 / 软删与过期不拦截 / 重建与 TTL 刷新 /
  **DB 访问计数器断言请求处理期间查询次数为 0**

### B4 admin API

- 交付：§6.1 全部端点（挂件 handler 范式注册进 adminapi；方法校验沿用 POST/GET 约定）
- ★ 错误响应不泄露内部细节；鉴权走既有链路；变更后触发快照重建
- 验收：httptest 单测覆盖 CRUD/导入/参数校验/软删恢复

### B5 WebUI 管理页

- 交付：黑白名单管理 Tab（列表/新增/软删恢复/导入），对接 B4 API
- 验收：`-tags dev` 起服务浏览器验证；无法手工验证时交付 API 契约 + 前端调用说明

### B6 存量迁移与收尾

- 交付：现有外挂 `rules/ip_blacklist.txt` 的 403 条风险 IP 批量导入 DB
  （`title` 标注来源，`block_type` 默认 1，可按行为批量归类）
- ★ 迁移完成后**外挂文件瘦身为最小种子集**（保留表头注释 + 若干示例，或清空仅注释）：
  DB 表成为唯一权威，避免同一 IP 双来源（外挂 + DB）导致管理面无法删除外挂条目；
  外挂文件仅作无 DB 部署（未启 DB）场景的离线兜底
- ★ 全量测试 + vet 通过；文档（CONFIGURATION/COMPONENTS/DEV_HANDBOOK）补 DB 黑白名单说明
- 验收：导入后 `SELECT count(*)` 与源文件条数一致；抽查无重复（唯一约束兜底）

---

## 8. 未来方向（本期不做，记录备查）

- **自动收录：高频请求自动拉黑**——按时间窗统计 IP 请求数（如按日 Top IP），超阈值自动入黑名单
  （需防误伤与阈值策略）；
- **自动收录：规则命中自动拉黑**——URL/UA 规则命中 N 次后自动拉黑该 IP；
- **每日请求统计报表**——按日聚合 Top IP / 拦截类别分布；
- **429 限流式封禁**——黑名单命中按策略返回 429 而非固定 403；
- **TTL 刷新参数化**——当前默认 60s，后续可配（CDN 前置可调小、网关可调大）；
- **攻击证据归档逻辑**——`attack_archive` 表已建，后续接入自动/人工归档触发与查询；
- **白名单配置迁移入表**——当前 `.env` 与表并存，后续可迁移收敛；
- **黑名单过期自动清理**——后台定时清理 `expires_at` 过期条目（索引已备）。
