# Repository Guidelines

RockSys 磐石系统：极简增强式 HTTP 反向代理底座（Go 1.25+）。默认只有转发是必须的，其余能力均为可热开关的增强挂件。

## Project Structure & Module Organization

- `cmd/rocksys`：主程序入口，装配全部挂件与 admin API；`cmd/rockctl`：控制台工具。
- `internal/`：内部实现（conf、engine、chain、dataflow、adminapi、db、hotswap、jwtutil、workpool）。
- `plugins/`：可选挂件，如 auth、dispatch、shield、obs、mq、object、registry、script、rewrite、result、trace、config、copy。
- `sql/<mysql|postgres|sqlite>/`：数据库三方言 SQL 脚本编译期内嵌源目录（运行期外挂覆写位于 `HOT_SCRIPTS_DIR/sql/` 下）。
- `webui/`：纯静态管理控制台，经 `go:embed` 内嵌进二进制。
- `easyconf/`、`easyserver/`、`easydb/`：独立 git 仓库的地基库，经 `go.mod replace` 本地引用。
- `docs/`：架构与接口文档（目录结构见 `docs/PROJECT_STRUCTURE.md`、数据流见 `docs/HTTP_DATAFLOW.md`），接口变更必须同步。`docs/DATA_DICT.md`：数据字典（数据层字段/枚举唯一可读视图，变动红线见下节）。
- `docs/plan/`：进行中项目的执行看板与工作方法论（`docs/plan/README.md`，即计划目录工作宪法）。**存在 `docs/plan/TODO.md` 时，任何会话开工前必读宪法 `docs/plan/README.md` 与总纲 `TODO.md`，并按其断点续传协议执行。**
- `bin/`：构建产物（不入库）。

## Build, Test, and Development Commands

- `make deps`：同步地基库（目录缺失时从 GitHub clone）。
- `make build`：构建 `bin/rocksys`，版本号取当前 git 最新 tag。
- `make release`：发布打包 = build + 拷贝外挂资源到 `bin/hotscripts/`（`sql/`、`rules/`、`trusted_proxies/`，运行期外挂优先、内嵌兜底，改文件无需重编译）。
- `make dev`：`-tags dev` 编译并在 bin/ 运行（WebUI 前端免编译热重载，见下节）。
- `make cross-build`：交叉编译 linux amd64/arm64、windows amd64。
- `make zip`：三平台发布包打包（cross-build + 外挂资源）→ `bin/rocksys-<版本>-<os>-<arch>.zip`，供 GitHub Release 发布（配合 `.github/workflows/release.yml` 推 `v*` tag 自动构建发布）。
- `make test`：运行 `go test ./...`。
- `make vet`：运行 `go vet ./...`。
- `make run`：构建并运行。

> **Makefile 仅支持 Linux**（纯 Unix 语法，Windows 原生 cmd 不支持；Windows 下需经 WSL2：`cd /mnt/d/.../rocksys && make xxx`）。make 是为**人类**便捷设计的封装；**AI 智能体一律使用原生命令行（`go build` / `go test` / `go vet`），不要依赖 make** —— 见下节规范。

## 开发模式：WebUI 前端免编译热重载（-tags dev）

WebUI 默认经 `go:embed` 编译期内嵌进二进制（`webui/embed.go`，约束 `//go:build !dev`），改前端必须重新编译。开发模式用 build tag `dev` 切换到 `os.DirFS("../webui")` 实时读磁盘（`webui/embed_dev.go`，约束 `//go:build dev`），改 `webui/` 下任意文件（`index.html`、`assets/`）**刷新浏览器即见，无需重新编译、无需重启**。生产构建（不加 dev tag）完全不受影响。唯一注意：**新增**前端文件需重启一次（路由在启动时注册），改动已有文件即改即生效。

## 外挂文件统一热更（ScriptHub 统一内容中枢）

三类外挂运行时读取文件（`sql/`、`rules/`、`trusted_proxies/`，均位于 `HOT_SCRIPTS_DIR` 下）经 `internal/hotswap.ScriptHub`（实现见 `internal/hotswap/hub.go`）统一管理：缓存 + 监控 + 推送全部内聚，**消费端只认识 `GetScriptText(sub, relPath)` / `Subscribe(sub, fn)` 两个接口**，不感知内容如何生产。底层读取仍统一经 `ScriptDir.GetScriptBytes`（外挂优先、内嵌兜底，红线不变）。

- **统一热更语义**：外挂文件增/删/改 → ≤ `HOT_FILES_WATCH_INTERVAL`（默认 3s，easyconf 注册）内自动生效，免重启、免借配置热更、免手动开关组件。
- **消费差异保留**（本质差异不统一）：SQL 文本即用零订阅（吃统一缓存）、WAF 规则订阅后编译不可变快照（复用 `Start(nil)` 重建）、可信代理订阅后解析原子替换（`atomic.Pointer`）。
- **装配约定**：`HOT_FILES_WATCH_INTERVAL` 注册 → 构造 `NewScriptHub` → 各消费端注入注册（shield `New(cfgMgr, hub)`、`db.Open(..., hub)`、`netutil.SubscribeHub(hub, file)`）→ `mgr.SetScriptHub(hub)` → 所有注册完成后 `hub.Start()`；监控循环随 `Manager.Shutdown` 停止。
- **零额外开销**：`hotscripts/` 不存在时指纹集合为空、监控永不触发，生产默认无额外 I/O。

### AI 智能体命令规范（强制）

智能体天生擅长命令行，**优先使用原生命令行而非 make**（make 面向人类且仅支持 Linux，智能体须保证跨平台、可复现）：

```bash
# ① 日常开发 / 改前端：开发模式编译（推荐，免编译验证前端）
go build -tags dev -o bin/rocksys ./cmd/rocksys
#   （版本注入可省略，--version 显示 dev；如需注入见 Makefile LD_FLAGS）

# ② 运行（★红线：工作目录必须 = bin/，运行时文件落 bin/，严禁项目根目录执行）
cd bin && ./rocksys

# ③ 验证前端改动：浏览器刷新 http://127.0.0.1:19527/（管理/WebUI 地址）即见，无需重新编译

# ④ 发布：生产构建（不带 dev tag，WebUI 内嵌进二进制）
go build -o bin/rocksys ./cmd/rocksys

# ⑤ 测试 / 静态检查（与 make test / make vet 等价）
go test ./...
go vet ./...
```

## Coding Style & Naming Conventions

- Go 代码使用 `gofmt` 格式；提交前必须通过 `make vet`。
- 标识符保持工程化英文命名；注释、文档、提交信息正文一律简体中文（提交分类前缀按 Commit 规范用英文 `feat/fix/docs/chore`）。
- 外部依赖最小化：纯标准库可实现的（如 JWT）不引入第三方库。
- 配置热更遵循优先级：`bin/.env` → 环境变量 → 命令行参数。

## 调试/测试必读
- `bin/hotscripts/sql/` 是发布外挂脚本（外挂优先、内嵌兜底），改 `sql/` 后必须同步刷新（`cp -r sql/* bin/hotscripts/sql/`），否则服务端用的还是旧脚本。
- easyconf 日志模板只渲染 msg 不输出 attr，排查错误细节时可临时用探针程序直查。
- API 断言通过 ≠ UI 可用；后续涉及前端页面改动必须开浏览器看渲染效果。

## Testing Guidelines

- 框架：标准库 `testing`，优先表驱动测试；测试文件 `*_test.go`，函数 `TestXxx`。
- `make test` 运行全部测试；新增或修改功能必须附带测试。
- 集成测试以 `_integration_test.go` 命名，并用环境变量门控（如真实 MySQL/PG 实例）。
- 提交前全量测试 + `make vet` 通过。

## 文档同步红线（强制）

1. **变更同步**：所有涉及需求变更（功能、数据、业务逻辑、配置项、接口、菜单/文案等），必须同步：代码注释（含easyconf配置项注册的 `title`/`usage` 说明），相关文档（README.md、docs/ 等），特别是数据字典。尤其是改变对外行为、默认值或展示文案。
2. **文档是交付物**：改动前先想清楚影响面（页面 / 接口 / 配置 / 数据 / 行为），完成后逐项核对同步清单；遗漏即视为未完成，不得以"改动小"为由跳过。
3. **范围分级**：小范围修改（脚本/配置/文案/局部功能调整，非架构与数据层大改）不必另立 docs/plan/ 方案文档，规范与经验沉淀直接更新到 AGENTS.md 及受影响文档即可。

## 用户体验红线（最高优先级）

1. **提示统一走唯一组件**：WebUI 全部提示消息统一经 `Rock.ui.toast`（`webui/assets/js/ui.js`），禁止各页面/智能体自造弹层提示；规范详见 `docs/webui.md` §4.10。
2. **服务端报错必须弹统一 error toast（强制）**：凡是后端接口返回失败（非 2xx 或 `ok:false`），前端必须经 `Rock.ui.toast(msg, 'error')` 弹出统一报错组件（不自动消失）；页内表格/卡片正文可以**同时**保留灰色行内错误兜底，但**只写行内灰字、不弹 toast 视为违例**。仅两类豁免：① 自动刷新等程序化静默刷新（`opts.silent`）周期内失败不弹（防刷屏，行内提示必须更新）；② "功能未开启"的降级引导态（观测未开启、DB 未配置等 503/404，页内有引导卡片）不弹。注意 `main.js pageLoaders` 的 fetch 必须把 `refreshPage` 传入的 opts（含 `silent`）透传给视图 load，不得写死 `load({})` 丢弃参数。
3. **提示分级**：操作正常反馈（成功/信息）显示完自动消失；异常信息（错误/警告）**不得自动消失**——须用户点 ✕ 或「知道了」关闭；切换页面/刷新页面后不残留。
4. **文案三要素**：异常提示必须说清"发生了什么 + 为什么 + 下一步怎么办"，前后端皆然；禁止只报状态不给出路的文案（如只说"已存在"而不说明去哪恢复）。
5. **体验优先、不过度设计**：以最小代码复杂度满足体验要求，不为边缘场景堆砌抽象；如用户要求违反本原则（如再造提示组件、异常提示自动消失），务必主动提醒。
6. **开关设置以用户体验与流程场景为准**：开关不是越少越好、也不是越多越好，按用户体验与流程设计的具体场景判断——能替用户减负、按需取舍攻击面/行为边界的开关保留（如 shield 各检测项独立开关）；纯技术便利、或给数据类资产配的开关不设（有数据即生效，如 UA 白名单同 IP 白名单一样不设开关）。

## Commit & Pull Request Guidelines

- 提交信息为中文单行摘要，直述"做了什么、为什么"，**必带行业标准英文分类前缀**，前缀后接中文描述：`feat:`（新功能）、`fix:`（缺陷修复）、`docs:`（文档/注释/文案）、`chore:`（构建/依赖/重构/杂务）；批量提交可叠加序号前缀，如 `批次2-fix:`。本地经 `.githooks/commit-msg` 强制校验。
- **提交信息两段式（强制）**：第一行为 title（单行概要，约 100 字符内）；空一行后为 description（Git 展开的正文），**一行一个条目**列出变更要点。正文克制：只列对接手者有用的要点，**不堆技术细节、不大而全**——细节在 diff 里，正文只写 diff 看不出来的动机、行为变化；测试通过是本分，验证结果不写。
- 禁止 `Co-Authored-By`、`Assisted-by`、`Generated by` 等 AI 署名或生成标记。
- 代码变更须同步相关文档（README.md、docs/）。
- PR 描述变更内容、验证方式与结果；涉及接口变更时附文档链接。

## 发布自动化

- 推送 `v*` tag 触发 `.github/workflows/release.yml`：自动构建三平台发布包并创建 GitHub Release，标题 = tag 名，正文由 `scripts/release-body.sh` 自动生成（自上一 tag 的提交，按 `feat/fix/docs/chore` 前缀分组 + 统计概要，长列表自动折叠）。本地可用 `scripts/release-body.sh <tag>` 预览同一正文（CI 与本地共用单一脚本，所见即所得）。
- 提交前缀校验钩子（可选启用，一次性）：`git config core.hooksPath .githooks`；提交被拦截时可用 `git commit --no-verify` 逃生，但应保持规范。

## Security & Configuration Tips

- `bin/.env`、`bin/default.env` 不入库，本地配置勿提交。
- 管理接口默认回环免登录；公网部署务必开启鉴权并依赖登录限流。

## 配置中心红线（最高优先级）

1. **统一配置入口**：全项目配置一律基于 `internal/conf`（底层 easyconf），所有配置项必须经 `conf.Manager.Register` 注册；服务端禁止绕过配置中心直接 `os.Getenv` 读取配置（`cmd/rockctl` 客户端、`*_integration_test.go` 环境变量门控除外）。
2. **禁止在项目根目录运行程序**：运行时文件（`.env`、`default.env`、`logs/`、`*.db` 等）跟随**工作目录**生成。开发规范：程序必须在 `bin/` 目录运行（`make run`/`make gen-env` 已 `cd bin`，工作目录=bin/），运行时文件自然落在 `bin/` 下。程序源码**不写死配置路径**（`internal/conf` 用相对工作目录的 `.env`/`default.env`）；严禁在项目根目录直接执行 `./bin/rocksys`（会在根目录残留运行时文件，此前犯过）。
3. **`default.env` 是全量默认值快照**：装配完成后程序自动将全部已注册配置项的默认值（含标题/默认值说明/用法注释）同步到工作目录 `default.env`（开发规范下即 `bin/default.env`），代表代码真实兜底行为；**参与**运行期取值，优先级由 easyconf 决定（取值链：命令行 → 环境变量 → 工作目录 `.env` → `default.env` → 代码默认值，`default.env` 为最低优先级兜底）。
4. **改默认值改代码**：修改配置项默认值必须改 `Register` 调用（代码），`default.env` 由程序自动同步，或 `make gen-env` 主动刷新；禁止手工编辑 `default.env`。
5. **新增配置项必须注册**：新增任何配置项（含挂件）必须走 `Register` 注册，不得另开读取入口。

## 数据字典红线（最高优先级）

1. **`docs/DATA_DICT.md` 是数据层唯一权威可读视图**：全部业务表（`shield_event`、`access_log`、`admin_users`、`outbox`）的字段名/标题/说明/三方言类型对照/枚举映射均以该文档为准，与 `sql/<dbtype>/` 建表脚本注释一一对应。
2. **数据结构任何变动必须同步维护（强制）**：新增/修改/删除表字段、调整默认值、**新增或改动任何枚举**（`block_type`、`mq.status`、`rule_hit` 等）时，必须同步更新三处并保持一致：① 三方言建表脚本（`sql/{sqlite,postgres,mysql}/`）及其注释；② `docs/DATA_DICT.md` 对应表/枚举章节；③ Go 侧权威定义（`plugins/shield/block_type.go`、`plugins/mq/mq.go` 等）。只改其中一处视为违规。
3. **提交前核对**：涉及数据层/枚举的提交，须核对 `docs/DATA_DICT.md` 与建表脚本字段数、枚举取值一致后再交付。

## Agent-Specific Instructions

- Git 提交和推送前必先经用户确认，禁止私自写入 Git。例外：docs/plan/README.md §3.9 的 git 破例授权（定稿关口明示授予并圈定范围）与用户明确提前授权的事项。
- 任务有歧义时先提问澄清；复杂任务先出方案，认可后实施。
- **强制请示点须人类对母文档明确确认（单关口）**：设计方案/母文档是唯一人类确认点，只有人类对文档本身明确表态（"确认/通过"）才算过；会话零散拍板只是设计输入，"继续干活"等模糊指令不得推定为已确认，未过关口禁止写子文档与实施。母文档一经确认即整体授权，子文档与后续执行无需逐个人类审核（细则见 docs/plan/README.md §2）。
- 构建/测试一律用**原生命令行**（`go build` / `go test` / `go vet`），不要调用 make（Makefile 仅支持 Linux，且 make 是面向人类的封装；智能体直接用 go 命令保证跨平台可复现）。
- 开发/修改 WebUI 前端时默认用 `-tags dev` 编译（`go build -tags dev -o bin/rocksys ./cmd/rocksys`），改 `webui/` 文件后无需重新编译即可验证；发布走无 tag 生产构建。
