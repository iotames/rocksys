# UA(爬虫)白名单 设计文档

> 状态：待确认（等待人类对本文档明确确认；确认后建 `docs/plan/TODO.md` 总纲与 `docs/plan/ua_whitelist/STEPn_*.md` 步骤文档，随后自主执行）。

## 1. 背景与现状结论（带证据）

shield 组件的 WAF 检测链（`plugins/shield/shield.go` `runWAF`）末环是爬虫/扫描器 UA 拦截（开关 `SHIELD_WAF_CRAWLER_UA`，默认 false）：

- 黑名单特征文件 `plugins/shield/rules/crawler_ua.txt`：小写子串匹配（`rules.go` `parseRuleLines` 统一小写化，`waf.go` `hasCrawlerUA` 子串命中）；含 `bot`/`spider` 等宽泛模式，一旦开开关会连带命中 googlebot/bingbot 等主流搜索引擎爬虫，造成 SEO 误拦风险；空 UA 也直接命中（`waf.go:85` 注释）。
- IP 黑白名单是全组件级豁免/拦截（`shield.go` `Handle` 内联判定 + `ipSet.contains`，:526-534：白名单优先、命中直接放行绕过包括 WAF 在内的整个 shield），用户认可该设计；但 UA 维度只有"拦"没有"放"。
- 规则文件热更体系已完备：`rules.go` `loadLines` 经 `hotswap.ScriptHub`（外挂 `HOT_SCRIPTS_DIR/rules/` 优先、内嵌兜底，≤3s 热更）；管理端点三件套 `GET /admin/shield/rules`、`GET /admin/shield/rules/file?name=`、`POST /admin/shield/rules/save`（`rules_admin.go`，可编辑文件白名单 `ruleFileMetas` 目前 6 个）。
- WebUI「WAF安全」页三主页签：攻击拦截 / 黑白名单 / 文件编辑（`webui/assets/js/views/waf.js`）；黑白名单页签内现有两个子页签「黑名单/白名单」（`blacklist.js`，DB 表 CRUD）；文件编辑页签经 `fileEditor.js` 公共工厂可在线编辑全部规则文件。
- **历史教训（用户点名）**：IP 白名单存在「.env `SHIELD_IP_WHITELIST` ∪ DB 表」双来源，被用户定性为错误设计（easyconf 不是垃圾桶，不是什么都能往 .env 塞）。本期 UA 白名单**只走规则文件单来源**，不新增任何配置项、不建 DB 表。

## 2. 已确认决策表（只追加不覆盖）

| # | 决策点 | 结论 | 理由 |
|---|--------|------|------|
| D1 | 实现路径 | 方案 A：纯规则文件（`rules/ua_whitelist.txt`），ScriptHub 热更；零 DB 表、零新配置项、零新管理端点 | UA 名单是规则而非配置；复用现成热更/编辑体系，最小复杂度 |
| D2 | 数据来源 | 仅规则文件单来源（内嵌兜底 + `HOT_SCRIPTS_DIR/rules/` 覆写），**不进 .env** | 用户明确：IP 白名单的 env∪DB 双来源是错误设计，easyconf 不是垃圾桶，不学它 |
| D3 | 判定优先级 | UA 白名单 > UA 黑名单 | `crawler_ua.txt` 含 `bot`/`spider` 宽泛模式，白名单优先才能放行 googlebot；与 IP 白名单优先精神一致 |
| D4 | 作用域 | **严格限定**：命中白名单仅豁免「爬虫 UA 拦截」这一环（含空 UA 拦截不受影响——空 UA 不会命中任何非空白名单模式）；方法白名单、体积限制、风险路径、路径遍历、SQL/XSS、限流、IP 黑白名单、自动封禁一律照常 | 用户强调"用户要为自己的操作负责"的边界：带 SQL 注入特征的搜索引擎爬虫照样拦 |
| D5 | 白名单文件名 | `ua_whitelist.txt`（对称黑名单既有 `crawler_ua.txt`） | 用户拍板 |
| D6 | 黑名单文件是否改名 | **不改名**，继续用 `crawler_ua.txt` | 用户已部署的外挂覆写文件不失效；页面显示名改为「UA(爬虫)黑名单」即可 |
| D7 | 默认白名单内容 | 保守清单：googlebot、bingbot、duckduckbot、applebot、baiduspider、sogou、yandex、slurp、facebookexternalhit；每条爬虫用**独立注释行**备注归属（★不能行内尾注：`parseRuleLines` 只忽略整行 `#`，行内尾注会混入模式）；文件头写明子串匹配/白名单优先/仅豁免爬虫 UA 拦截/其余检测照常；用户可随时注释行首 `#` 禁用单条（热更 ≤3s） | 用户拍板 + 实现约束（行内注释不兼容） |
| D8 | WebUI 结构 | 「黑白名单」页签子页签由 2 个改 4 个：IP黑名单 / IP白名单 / UA(爬虫)黑名单 / UA(爬虫)白名单。IP 两个沿用现有 DB CRUD 视图；UA 两个做**简单查看 + 追加**（无 DB 表，读写规则文件三端点），页内提示"完整编辑请前往「文件编辑」页签"并带跳转链接；「文件编辑」页签保留不动 | 用户拍板；文件编辑页签是全量规则文件统一入口，保留 |

## 3. 设计方案（文件/端点/字段级）

### 3.1 数据与规则加载（后端）

- 新增内嵌规则文件 `plugins/shield/rules/ua_whitelist.txt`，内容按 D7：

  ```
  # UA(爬虫)白名单：每行一个模式，匹配时转小写（子串匹配）。
  # 优先级：白名单 > 黑名单；命中白名单仅豁免「爬虫/扫描器 UA 拦截」这一步，
  # 方法白名单、体积限制、风险路径、路径遍历、SQL/XSS、限流、IP 黑白名单、自动封禁照常生效
  # （搜索引擎爬虫若带攻击特征照样拦）。需要禁用某条放行时，在该行行首加 # 即可（热更 ≤3s 生效）。
  # 以 # 开头的行是注释，空行忽略。外挂覆写目录 HOT_SCRIPTS_DIR/rules/ 同名文件可覆盖本文件。
  # ── 搜索引擎爬虫 ──
  # Google 搜索
  googlebot
  # Bing 搜索
  bingbot
  # DuckDuckGo 搜索
  duckduckbot
  # Apple（Safari/iCloud 推荐/ Spotlight）
  applebot
  # 百度搜索（Baiduspider 及 -render/-image 等变体，子串均覆盖）
  baiduspider
  # 搜狗搜索
  sogou
  # Yandex 搜索
  yandex
  # Yahoo 搜索
  slurp
  # ── 社交平台预览 ──
  # Facebook 分享链接预览
  facebookexternalhit
  ```

- `plugins/shield/rules.go`：新增常量 `ruleFileUAWhitelist = "ua_whitelist.txt"` 与 `RuleSet.UAWhitelist []string` 字段，`load` 中 `loadLines` 加载（与 CrawlerUA 同路径）。
- `bin/hotscripts/rules/ua_whitelist.txt`：`make release` 资源同步（开发期手工 `cp -r rules/* bin/hotscripts/rules/`——注意仓库根的 `rules/` 目录不存在，内嵌源在 `plugins/shield/rules/`，同步命令以实际布局为准）。

### 3.2 WAF 判定（后端）

- `plugins/shield/waf.go`：`wafSnapshot` 增加 `uaWhitelist []string`；新增方法 `uaWhitelisted(ua string) bool`（小写化后子串匹配任一模式；模式列表空直接 false）。
- `plugins/shield/shield.go`：
  - 快照构建（约 :405 附近）注入 `uaWhitelist: rs.UAWhitelist`；
  - `runWAF` 第 7 步改为 `if waf.crawlerEnabled && !waf.uaWhitelisted(ua) && waf.hasCrawlerUA(ua)`——白名单优先（D3），仅豁免该步（D4），空 UA 因不命中任何非空模式仍被拦。
- `plugins/shield/rules_admin.go`：`ruleFileMetas` 增加 `{ruleFileUAWhitelist, "UA(爬虫)白名单", "SHIELD_WAF_CRAWLER_UA 开启后命中即豁免爬虫 UA 拦截（白名单优先，仅豁免该步，其余检测照常）"}`；`crawler_ua.txt` 的 title 由「爬虫 UA 特征」改为「UA(爬虫)黑名单」（desc 同步点明白名单优先）。文件编辑页签白名单由 6 个变 7 个。

### 3.3 WebUI（前端）

- `webui/assets/js/views/waf.js`：主 Tab 结构不变（攻击拦截 / 黑白名单 / 文件编辑）；`iplist` 子视图的子页签改为 4 个：`ipblack` / `ipwhite` / `uablack` / `uawhite`。前两个路由到 `blacklist.js`（kind black/white 不变），后两个路由到新模块 `ualist.js`。
- 新增 `webui/assets/js/views/ualist.js`（借鉴 IP 白名单页的简单形态，无 DB）：
  - **查看**：`GET /admin/shield/rules/file?name=<ua_whitelist.txt|crawler_ua.txt>` 展示当前生效内容（保留注释原样展示，标注外挂覆写/内嵌状态与生效行数——清单接口 `GET /admin/shield/rules` 已含这些字段）；
  - **追加**：单行输入框 + 追加按钮 = 取当前生效文本 → 末尾插入新行 → `POST /admin/shield/rules/save` 整体保存（原子写，服务端落 `HOT_SCRIPTS_DIR/rules/<name>`，≤3s 热更）；输入为空/纯空白拒绝；重复模式（忽略注释与大小写）提示已存在不重复追加；整文保存为 last-write-wins，不做并发合并（单管理员场景接受，不做加锁/版本号机制）；
  - **入口提示**：卡片副标题固定文案"完整编辑请前往「文件编辑」页签"，链接点击仅切换到 `files` 主页签（**不做文件预选**：`ruleFiles.js` 的文件选中为模块局部状态、未导出钩子，预选须新造机制，不符合最简原则；用户落地后自行在文件列表点选目标文件）；
  - 服务端保存失败按体验红线弹 `Rock.ui.toast(msg,'error')`。
- `main.js` 的视图存在性校验数组追加 `'ualist'`。
- 文案统一「UA(爬虫)黑名单 / UA(爬虫)白名单」。

### 3.4 明确不做（边界）

- 不新增任何配置项（`default.env` 不变）、不建 DB 表、数据字典零变更；
- 不改 IP 黑白名单任何行为（含既有的 env∪DB 双来源——历史问题，本期不扩大整改面）；
- 不做 UA 白名单的删除/行级编辑 UI（走「文件编辑」页签整文编辑）；不做自动发现/验证搜索引擎爬虫（反解 DNS 等不在本期）；
- 不动 `SHIELD_WAF_CRAWLER_UA` 默认值（仍 false）。

## 4. 验收标准

1. `go test ./...` 与 `go vet ./...` 全绿；`plugins/shield` 新增测试覆盖：
   - 白名单命中 → 开启爬虫 UA 拦截时放行该步；
   - 白名单未命中 → 照拦（403 + 记 `crawler_ua` 事件）;
   - 优先级：UA 同时命中白/黑名单模式（如 `googlebot` 命中白名单、也被黑名单 `bot` 覆盖）→ 放行；
   - 严格作用域：白名单 UA 携带 SQL 注入查询串 → SQL 检测仍拦；
   - 空 UA：不在白名单 → 照拦；白名单列表为空 → 行为与现状完全一致（回归）。
2. 规则加载测试：`ua_whitelist.txt` 解析（小写化、忽略整行注释与空行）；`ruleFileMetas` 含 7 个文件、`GET /admin/shield/rules` 清单可见新文件。
3. 浏览器实操（`-tags dev` 构建、`bin/` 运行）：黑白名单页签四子页签渲染；UA 黑/白名单页查看与追加保存闭环（追加 → ≤3s 后该模式生效）；「文件编辑」页签可见并可编辑 `ua_whitelist.txt`；UA 页签跳转链接可达；保存失败弹统一 error toast。
4. 端到端语义验证（可 curl 复现）：`SHIELD_WAF_CRAWLER_UA=true` 下，UA 含 `Mozilla/5.0 (compatible; Baiduspider/2.0; ...)` 的普通请求 200；UA 含 `sqlmap/1.8` 的请求 403；UA 含 `Baiduspider` 且查询串带 SQL 注入特征的请求 403（`sql_pattern` 事件）。
5. 文档同步核对：`docs/webui.md`（WAF 页结构、功能表、子页签）、`plugins/shield/doc.go` 或组件 README（若提及规则文件清单则补 `ua_whitelist.txt`）、`README.md` 涉及段落；数据字典确认零变更（仅核对，不改）。

## 5. 变更记录

- 2026-08-31：初稿定稿。决策 D1–D8 均经用户逐条确认；用户补充百度蜘蛛 UA 格式资料（PC/移动/渲染/图片变体共同特征为含 `Baiduspider` 子串），已由 D7 的 `baiduspider` 小写子串模式覆盖全部变体。
- 2026-08-31：复核修订——§1 `matchIP` 更正为 `Handle` 内联判定（代码无 matchIP 函数）；§3.1 `loadAll`→`load`、`ruleSet`→`RuleSet`（以代码为准）。
- 2026-08-31：质量评估修订（五维：最佳实践/UX/改动量/复杂度/架构统一性，评估通过）——落实三点：① 预选钩子问题定案：核实 `ruleFiles.js` 无可复用文件选中状态，入口链接**仅切页签、不做预选**（新造钩子不合最简原则，消除实施发散点）；② 并发语义定案：整文保存 last-write-wins、单管理员场景接受，不做加锁/版本号（多用户并发不在本期范围）；③ 黑名单宽泛模式（如 `slurp`）不做收紧：UA 是君子协议，本就防不住伪造 UA 的攻击者，白名单仅豁免爬虫拦截一环、无安全回归。
