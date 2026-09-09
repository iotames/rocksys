# STEP7：首页概览「流量统计」区（前端）

状态：待实施

## 目标
按 PLAN §3.5：views/overview.js 新增页面局部「流量统计」卡（不动顶栏/菜单/运行指标区）：
- 时间范围：预设 近24小时/今日/近7天/近30天 + dateRange 自定义；默认近24小时；本地时间换算 UTC 传参
- 指标卡 grid：请求/PV/UV/独立IP、拦截/攻击IP、4xx 数率/4xx 拦截数率、5xx 数率（口径按 §3.4，标签写明）
- 访问情况/拦截情况折线（Rock.comp.chart）+ 峰值（序列 max）；桶标签 UTC 原样展示并标注 UTC
- 地理位置卡：Top 国家列表 + 访问/仅拦截切换；geo 缺失页内常驻警告引导卡（D17）
- 降级：obs 未启用 503 → 引导态不弹 toast（豁免②，行内灰字）；拦截侧 null → "—"
- toast/silent/文案三要素遵守红线；main.js pageLoaders 透传 opts

## 改动文件清单
- webui/assets/js/views/overview.js
- webui/assets/js/main.js（pageLoaders 透传，如现状已透传则不动）
- 可能新增局部组件文件（实施时定，新增文件需重启 dev 进程一次）

## 实施步骤（完成一项立即勾选保存）
- [ ] 时间范围组件接线（预设+自定义+UTC 换算）
- [ ] 指标卡 grid（率计算/分母 0/“—”兜底）
- [ ] 双折线 + 峰值
- [ ] geo 卡 + 缺失引导卡
- [ ] 降级路径与 silent 透传核对

## 验证
- 接手核实命令：`grep -n "流量统计" webui/assets/js/views/overview.js | head`
- 本步完整验证（必须实看渲染）：
  - [ ] dev 构建运行，浏览器实测：四预设出数、口径互洽（请求次数=访问+拦截各桶和）、缓存 computed_at 不变、TTL=0 变化、geo 缺失引导卡
  - [ ] 截图/视觉核验记录到回填区

## 完成标准
- 浏览器实测全过且渲染正常；口径互洽。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 改动视图：overview.js（流量统计区）
### 偏差与现场记录
- （无）
