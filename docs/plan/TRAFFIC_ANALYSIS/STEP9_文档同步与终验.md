# STEP9：文档同步 + hotscripts 同步 + 全量终验

状态：待实施

## 目标
按 PLAN §3.6 与验收 §5 收口。

## 实施步骤（完成一项立即勾选保存）
- [ ] docs/webui-api.md：三端点 + window 参数
- [ ] docs/webui.md：概览流量统计区 / WAF 卡
- [ ] docs/CONFIGURATION.md：GEOIP_MMDB_DIR、OBS_TRAFFIC_CACHE_TTL
- [ ] docs/COMPONENTS.md / README.md：geo 能力与统计报表
- [ ] docs/PROJECT_STRUCTURE.md：internal/geoip 与调用链路
- [ ] docs/DATA_DICT.md 终核（与 STEP1 产出一致性复核）
- [ ] bin/hotscripts/sql 全量同步 ✓（diff 核对）
- [ ] 终验：go test ./... + go vet ./... + 生产构建（无 tag）+ dev 浏览器实测清单（验收 §5.2）+ 老库升级路径（§5.3）+ 真库门控（§5.1）
- [ ] 验收结论回写 PLAN 变更记录；临时决策（若有）呈报；项目状态改「待人类验收」

## 验证
- 接手核实命令：`git status --short`（无未提交产物）`&& go test ./... && go vet ./...`
- 完成标准：验收 §5 全绿或如实写入已知边界。

## 实施回填区
### 偏差与现场记录
- （无）
