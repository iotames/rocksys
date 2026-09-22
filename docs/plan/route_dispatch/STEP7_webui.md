# STEP7：WebUI 路由分发页

状态：已实施

## 目标

落地「路由分发」页（`#/dispatch`，侧边栏独立顶级项，性质同「脚本」页）：状态区（规则源/计数/重载按钮 + DISPATCH_ENABLED 未开启引导卡，豁免 toast）+ 三视图切换（规则/均衡器/节点）+ 命中测试器（规则视图右侧常驻卡）+ 三组表单弹层 + 健康三态展示。交互细节唯一依据 DESIGN「WebUI 设计」章节（表单弹层三条的完整口径：序号钳 999 与同序号非阻断提示、least_conn 选中弹非阻断警告（缓冲路径代价与在途计数泄漏边界）、sticky 开启弹 WS 首连警告、节点探活折叠区提示等）。

## 改动文件清单

- 新增 `webui/assets/js/views/dispatch.js`：页面视图（三视图 + 状态区 + 命中测试器 + 表单弹层）
- 修改 `webui/assets/js/main.js`：pageLoaders 注册 `dispatch` 路由（约 :118 pageLoaders 表；load 须透传 refreshPage 的 opts 含 silent）
- 修改 `webui/index.html`：侧边栏新增「路由分发」项
- 修改 `docs/webui/pages.md`：新增路由分发页章节（含组件分层与交互规范说明）
- 复用公共组件：filterBar/dataTable/detailModal（pages.md §4.7）、`Rock.ui.toast` 唯一提示（§4.10）；meta 枚举经 `/admin/dispatch/rules/meta` 接口下发

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [x] dispatch.js 骨架：状态区（规则源状态、三计数、重载按钮）+ 三视图切换 + pageLoaders 注册 + 侧边栏项
- [x] 规则视图：filterBar（类型▾ 域名 标签▾ 状态▾ 关键词）+ dataTable 服务端分页（序号/域名/路径类型/路径值/标签/均衡器/启用开关/操作编辑恢复删除）
  - 实施口径偏差：筛选栏落地为「类型▾ 域名 状态▾ 关键词」（标签筛选待后端 list 接口下发 tags 后回补，见偏差记录）；表格无行内启用开关（见偏差记录）
- [x] 规则表单弹层：域名输入（失焦转小写、拒绝端口与通配、占位「留空=任意域名」）、类型下拉（meta）、路径值按类型切换占位与校验、序号（默认最大+10、超 999 钳制并提示、同序号非阻断提示并列出）、均衡器下拉（名称+节点数）、标签多选（下拉已有或回车新建，来源 tags 接口）、标题/备注
- [x] 均衡器视图 + 表单：名称、策略下拉（选中 least_conn 弹非阻断警告：缓冲路径代价与在途计数泄漏边界）、sticky 开关（开启弹 WS 首连拿不到 Cookie 警告，展开 Cookie 名输入）、节点关系编辑器（节点下拉显实时健康点 + 权重数字注记「仅 round_robin 生效」+ 高优/备份下拉 + 行增删；整组保存）、停用/删除时引用计数与影响提示
- [x] 节点视图 + 表单：名称、URL（`http(s)://` 校验 + 唯一性提示）、实时健康三态（绿红灰，灰=未探活，/health 接口；附探活窗口期说明文字）、探活折叠区（周期/超时/路径；路径留空显式提示「始终视为健康——宕机不自动摘除」）、删除被引用拒绝文案三要素
- [x] 命中测试器：Host/Path 输入 + 测试按钮 → 命中结果（规则摘要 → 均衡器 → 所选节点或兜底链位置）；未命中显示走默认后端
- [x] 引导态与降级：DISPATCH_ENABLED 未开启整页引导卡（开关位置 + 开启路径，豁免 toast）；DB 未就绪 503 按普通错误弹 error toast
- [x] pages.md 章节同步（页面结构、组件使用、提示与降级口径）

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go build -tags dev -o bin/rocksys ./cmd/rocksys && grep -c dispatch webui/assets/js/main.js`
  （预期：dev 构建成功；main.js 含 dispatch 路由注册）
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [x] `cd bin && ./rocksys` 启动后浏览器实看 `http://127.0.0.1:19527/#/dispatch`（dev 构建 + 真实 PG，主流程实看）——由主流程执行
  - [ ] 截图留证：三视图列表、三组表单弹层（含 least_conn 选中警告弹层、sticky 警告）、命中测试器命中/未命中两态、引导卡、错误 toast（服务端报错路径）——由主流程执行
  - [x] 实操回路：登记节点 → 建均衡器（关系编辑器选节点/权重/高优）→ 建规则（域名 a.com + 前缀 /api + 标签 prod，序号默认 10）→ 删除（确认弹层）→ 仅已删除筛选可见 → 恢复（toast + 回归活跃列表 + 计数联动）→ 重载快照，全部经真实端点与真实 PG
- [x] 缺陷修复回验：软删行列表不可见致「恢复」不可达（query_list 恒过滤 deleted_at IS NULL）——三方言 18 个脚本改 CASE 单参谓词（include_deleted 0=仅活跃 1=仅已删除）+ admin.go 三 handler 传参 + dispatch.js 状态筛选加「仅已删除」+ admin_test 新用例；UI 闭环复验通过", 1)——由主流程执行
  - [x] `go vet ./...` 通过（全量 `go test ./...` 亦绿）

## 完成标准

- 三视图 + 三表单 + 测试器 + 引导态全部实看验证，截图留证回填
- 提示全走 `Rock.ui.toast`；错误不自动消失；文案三要素
- 数据资产不设页面级开关（唯一开关是组件级 DISPATCH_ENABLED，仅引导提示）
- pages.md 章节与实现一致（文档同步红线）

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`webui/assets/js/views/dispatch.js`
- 修改文件：`webui/assets/js/main.js`（pageLoaders + 路由）、`webui/index.html`（侧边栏）、`docs/webui/pages.md`（新章节）
- 新增交互：三视图切换、命中测试器、节点关系编辑器、least_conn 选中非阻断警告与 sticky WS 首连警告

### 偏差与现场记录
- **规则表格无行内启停开关**：规则更新为整行语义（`/rules/update`），且列表行不含标签数据——行内 toggle 走 update 会把标签整组清空。启停收进编辑弹层的「启用」开关，与标签一并整组提交，无数据丢失风险。
- **筛选栏暂无「标签▾」、表格「标签」列暂留空**：`GET /admin/dispatch/rules` 未下发每行 tags（也无不带 rule_id 的 rule_tag 关系查询端点），纯前端无法回显/过滤；待后端 list 接口补 tags 字段后回补（前端表格 render 已按 `row.tags` 数组预留位，列暂以均衡器列之前空缺呈现）。pages.md 已按此口径记录为已知缺口。
- **DISPATCH_ENABLED 判定来源**：经 `GET /admin/switch/list` 找 `name=dispatch` 的 `state`，未开启渲染整页引导卡（豁免 toast）；开关状态不可知时不阻塞页面，状态区留空。
- **构建产物**：自验用 `go build -tags dev -o bin/rocksys.dev ./cmd/rocksys`（rocksys.dev 命名避免覆盖可能运行中的 bin/rocksys；是否替换由主流程决定）。
- 浏览器实看 / 截图 / 实操回路四项验证由主流程执行，保持未勾选。
- 【浏览器验证记录】2026-09-22 主流程实看通过（清单见验证节勾选）；验证数据（节点核验节点A/均衡器核验池A/规则 a.com:/api）保留供 STEP8 实请求终验复用。
- 【验收反馈修复 2026-09-22】人类验收提出 6 项，全部修复并实看验证：①弹层统一确认（全站仅 Rock.ui.openModal/detailModal/confirmDialog，入 pages.md §4.10）；②弹层内按下拖到遮罩松开误关（openModal/confirmDialog 改为 mousedown+click 双落点判定）；③节点 url 含路径可保存（validateNode 拒绝路径/查询/锚点，尾斜杠静默归一）；④警告类 toast 自动消失（去掉三处显式 duration，回归 ui.js warning 常驻语义）；⑤规则标签链路（后端列表下发 tags、保存去重、编辑态回显 chips、下拉过滤已选、选中即添加、列表加标签列、chip ✕ 样式修正）；⑥侧边栏独立项并入 dispatch 组件详情第三页签「路由管理」（消除菜单堆叠，懒挂载 + ?tab=routes 直达）。浏览器实看全部通过（截图留证会话）。
