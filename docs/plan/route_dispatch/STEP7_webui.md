# STEP7：WebUI 路由分发页

状态：待实施

## 目标

落地「路由分发」页（`#/dispatch`，侧边栏独立顶级项，性质同「脚本」页）：状态区（规则源/计数/重载按钮 + DISPATCH_ENABLED 未开启引导卡，豁免 toast）+ 三视图切换（规则/均衡器/节点）+ 命中测试器（规则视图右侧常驻卡）+ 三组表单弹层 + 健康三态展示。交互细节唯一依据 DESIGN「WebUI 设计」章节（表单弹层三条的完整口径：序号钳 999 与同序号非阻断提示、least_conn 选项 obs 门控与两级警告、sticky 开启弹 WS 首连警告、节点探活折叠区提示等）。

## 改动文件清单

- 新增 `webui/assets/js/views/dispatch.js`：页面视图（三视图 + 状态区 + 命中测试器 + 表单弹层）
- 修改 `webui/assets/js/main.js`：pageLoaders 注册 `dispatch` 路由（约 :118 pageLoaders 表；load 须透传 refreshPage 的 opts 含 silent）
- 修改 `webui/index.html`：侧边栏新增「路由分发」项
- 修改 `docs/webui/pages.md`：新增路由分发页章节（含组件分层与交互规范说明）
- 复用公共组件：filterBar/dataTable/detailModal（pages.md §4.7）、`Rock.ui.toast` 唯一提示（§4.10）；meta 枚举经 `/admin/dispatch/rules/meta` 接口下发

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [ ] dispatch.js 骨架：状态区（规则源状态、三计数、重载按钮）+ 三视图切换 + pageLoaders 注册 + 侧边栏项
- [ ] 规则视图：filterBar（类型▾ 域名 标签▾ 状态▾ 关键词）+ dataTable 服务端分页（序号/域名/路径类型/路径值/标签/均衡器/启用开关/操作编辑恢复删除）
- [ ] 规则表单弹层：域名输入（失焦转小写、拒绝端口与通配、占位「留空=任意域名」）、类型下拉（meta）、路径值按类型切换占位与校验、序号（默认最大+10、超 999 钳制并提示、同序号非阻断提示并列出）、均衡器下拉（名称+节点数）、标签多选（下拉已有或回车新建，来源 tags 接口）、标题/备注
- [ ] 均衡器视图 + 表单：名称、策略下拉（least_conn obs 门控：obs 未开启置灰点击无效并弹 toast 说明；obs 开启选中弹非阻断警告含在途计数泄漏边界）、sticky 开关（开启弹 WS 首连拿不到 Cookie 警告，展开 Cookie 名输入）、节点关系编辑器（节点下拉显实时健康点 + 权重数字注记「仅 round_robin 生效」+ 高优/备份下拉 + 行增删；整组保存）、停用/删除时引用计数与影响提示
- [ ] 节点视图 + 表单：名称、URL（`http(s)://` 校验 + 唯一性提示）、实时健康三态（绿红灰，灰=未探活，/health 接口；附探活窗口期说明文字）、探活折叠区（周期/超时/路径；路径留空显式提示「始终视为健康——宕机不自动摘除」）、删除被引用拒绝文案三要素
- [ ] 命中测试器：Host/Path 输入 + 测试按钮 → 命中结果（规则摘要 → 均衡器 → 所选节点或兜底链位置）；未命中显示走默认后端
- [ ] 引导态与降级：DISPATCH_ENABLED 未开启整页引导卡（开关位置 + 开启路径，豁免 toast）；DB 未就绪 503 按普通错误弹 error toast
- [ ] pages.md 章节同步（页面结构、组件使用、提示与降级口径）

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go build -tags dev -o bin/rocksys ./cmd/rocksys && grep -c dispatch webui/assets/js/main.js`
  （预期：dev 构建成功；main.js 含 dispatch 路由注册）
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [ ] `cd bin && ./rocksys` 启动后浏览器实看 `http://127.0.0.1:19527/#/dispatch`（dev 模式改文件刷新即见）
  - [ ] 截图留证：三视图列表、三组表单弹层（含 least_conn obs 门控两种态、sticky 警告）、命中测试器命中/未命中两态、引导卡、错误 toast（服务端报错路径）
  - [ ] 实操回路：登记节点 → 建均衡器（含关系编辑器）→ 建规则（含标签）→ 启停开关 → 软删/恢复 → 重载按钮，全部经真实端点（截图留证）
  - [ ] `go vet ./...` 通过

## 完成标准

- 三视图 + 三表单 + 测试器 + 引导态全部实看验证，截图留证回填
- 提示全走 `Rock.ui.toast`；错误不自动消失；文案三要素
- 数据资产不设页面级开关（唯一开关是组件级 DISPATCH_ENABLED，仅引导提示）
- pages.md 章节与实现一致（文档同步红线）

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`webui/assets/js/views/dispatch.js`
- 修改文件：`webui/assets/js/main.js`（pageLoaders + 路由）、`webui/index.html`（侧边栏）、`docs/webui/pages.md`（新章节）
- 新增交互：三视图切换、命中测试器、节点关系编辑器、obs 门控与两级警告

### 偏差与现场记录
- <设计与现实的偏差、试过放弃的方案、阻塞原因——先写这里再继续动手>
