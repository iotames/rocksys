# A8 · 全量验证与文档同步收尾（IP 黑名单项目）

> 实施状态：**待实施**
> 前置：A1-A7 全部已实施。

## 3. 实施步骤

- [ ] `go test ./...` 全量通过
- [ ] `go vet ./...` 无告警
- [ ] 生产构建 `go build -o bin/rocksys ./cmd/rocksys` 成功
- [ ] dev 构建 + 浏览器整包回归：A5/A6/A7 各自 §4 清单全过 + 自动拉黑实战触发（A4 §4）+ sync_file + 排序全选项 + 表结构页对 ip_blacklist 显示无差异（A1 已落列）
- [ ] `docs/DATA_DICT.md` 终核（红线）：ip_blacklist 字段数（含 warn_times）与三方言建表脚本一致；block_type 枚举 0-11 三处（脚本注释/字典/Go 定义）一致
- [ ] `docs/webui-api.md`：补 sync_file / ban / jail / 列表 sort 参数 / events in_blacklist 字段
- [ ] `docs/webui.md`：新交互规范章节（操作列与封禁弹窗/从文件同步/排序下拉/首页页签与小黑屋/自动拉黑说明）
- [ ] `docs/CONFIGURATION.md`：SHIELD_AUTO_BAN_* 四配置项（默认值/语义/热更说明）
- [ ] `docs/IP_BLACKLIST_PLAN.md`：状态行改「已实施」+ 变更记录收尾行
- [ ] `docs/plan/TODO.md`：A8 状态已实施 + 进度日志一行 + **向用户汇报两项目完成总结，等待 git 提交确认（不自行提交）**

## 4. 验证

- [ ] 全部命令与浏览器回归通过
- [ ] 文档逐份 diff 自查：无遗漏小节、接口签名与实现一致、默认值与代码一致

## 5. 完成标准

清单全勾 → 状态「已实施」→ 总纲收尾 → 汇报用户。

## 6. 实施回填区（中断现场记录）

（空）
