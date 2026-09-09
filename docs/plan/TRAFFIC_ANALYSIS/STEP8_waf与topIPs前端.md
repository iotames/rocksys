# STEP8：WAF 实时卡改造 + topIPs 地区列 + geo 警告 toast（前端）

状态：待实施

## 目标
按 PLAN §3.5：
- views/waf.js：窗口瓦片可切 1m/5m/15m/1h；「累计落库」→「本次运行落库（重启清零）」；新增「落库总数」瓦片（进页查一次不随窗口刷新）；卡片副标注「内存窗口数据重启后从零重新累计」
- views/topIPs.js：地区占位列接 geo（查询时解析结果）；缺失时占位 + 页内引导（D17 形态）
- geo 缺失 toast：依赖 geo 页面会话内首次进入弹一次统一警告 toast（sessionStorage 标记，不重复）

## 改动文件清单
- webui/assets/js/views/waf.js、views/topIPs.js
- geo 状态检测公用小工具（实施时定：components 或 utils 下，新增文件需重启 dev 一次）

## 实施步骤（完成一项立即勾选保存）
- [ ] waf 桶宽切换 + 瓦片改造
- [ ] topIPs 地区列
- [ ] geo toast 统一检测（sessionStorage）✓（浏览器实测）

## 验证
- 接手核实命令：`grep -n "本次运行落库\|落库总数" webui/assets/js/views/waf.js | head`
- 本步完整验证（必须实看渲染）：
  - [ ] 浏览器实测：桶宽切换、落库总数、topIPs 地区、toast 一次语义
  - [ ] 视觉核验记录到回填区

## 完成标准
- 浏览器实测全过；toast 不刷屏；文案三要素。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 改动视图：waf.js、topIPs.js
### 偏差与现场记录
- （无）
