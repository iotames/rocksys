# WAF 动态 IP 黑白名单存量迁移手册

> 适用：将既有外挂文件 `rules/ip_blacklist.txt` 中的存量风险 IP 批量导入数据库
> （WAF 黑白名单 DB 化的可选迁移步骤，见 `docs/DEV_HANDBOOK.md` §9.4.1）。
> 迁移是**部署环境的运行时动作**，仓库代码无法代执行——按本手册操作。
> 相关：`docs/DEV_HANDBOOK.md` §9.4.1（动态黑白名单管理）。

---

## 1. 前置条件

- 网关已启用 DB（`DB_DRIVER`/`DB_DSN` 配置有效），启动后自动幂等建表：
  `ip_blacklist` / `ip_whitelist` / `attack_archive`（三方言脚本见 `sql/<dbtype>/`）；
- 管理面可达（默认 `http://127.0.0.1:19527/`，回环免登录）；
- 确认当前外挂文件条目数（应约 403 条）：

```bash
grep -cE '^[0-9a-fA-F.:/]+$' plugins/shield/rules/ip_blacklist.txt
```

## 2. 提取 IP 清单

外挂文件含注释/空行，先提取纯 IP 列表（每行一个精确 IP/CIDR）：

```bash
grep -E '^[0-9a-fA-F.:/]+$' plugins/shield/rules/ip_blacklist.txt > /tmp/ip_list.txt
wc -l /tmp/ip_list.txt   # 应与上一步条数一致
```

## 3. 批量导入（幂等）

```bash
curl -s -X POST --data-binary @/tmp/ip_list.txt \
  'http://127.0.0.1:19527/admin/shield/blacklist/import?title=%E5%AD%98%E9%87%8F%E8%BF%81%E7%A7%BB&block_type=1'
# → {"ok":true,"imported":403,"skipped":0}（重复导入 skipped 递增、imported 为 0，幂等）
```

> 导入接口 body 为纯文本（每行一个 IP/CIDR），`title`（备注）与 `block_type`（拉黑类别，默认 1）
> 经 query 参数传递（前端经 `api.post` 调用时 body 为 JSON 字符串编码，后端两种均兼容）。
> 已存在的 IP 自动跳过（唯一约束幂等），可安全重复执行。
> 导入成功后网关**立即重建快照**，新增条目即刻生效（无需重启、无需等 TTL）。

## 4. 核对

- 管理面列表核对（总数应为 403）：

```bash
curl -s 'http://127.0.0.1:19527/admin/shield/blacklist?limit=1' | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])'
```

- 或直接查库（sqlite 为例）：

```bash
sqlite3 <DB_DSN> "SELECT count(*), count(DISTINCT ip) FROM ip_blacklist;"
# → 两条相等即无重复（唯一约束兜底）
```

## 5. 导入后：外挂文件瘦身（可选，建议）

迁移核对无误后，**DB 表即唯一权威**。为避免同一 IP 双来源
（外挂 + DB 并存导致管理面无法删除外挂条目），建议将 `rules/ip_blacklist.txt`
瘦身为最小种子集：保留表头注释与若干示例，或清空仅注释。

> ★ 瘦身前务必确认 `SELECT count(*)` 与源文件条数一致且无重复；
> 瘦身后外挂文件仅作**无 DB 部署**（未启 DB）场景的离线兜底。
> 若暂不瘦身，外挂 ∪ DB 取并集照常生效，行为不受影响（仅管理面可见性受限）。

## 6. 日常管理入口

| 操作 | 方式 |
|---|---|
| 新增单条 | WebUI「拦截统计 → 黑白名单」Tab，或 `POST /admin/shield/blacklist`（body: `{"ip":"1.2.3.4","title":"...","block_type":1}`） |
| 批量导入 | 本手册 §3（`POST /admin/shield/blacklist/import`） |
| 软删 / 恢复 | `POST /admin/shield/blacklist/delete` / `.../restore`（body: `{"id":N}`） |
| 更新 | `POST /admin/shield/blacklist/update`（body: `{"id":N,"title":"...","block_type":N}`） |
| 列表 | `GET /admin/shield/blacklist?ip=&block_type=&valid_only=&limit=&offset=` |

白名单同构（`/admin/shield/whitelist`，无 block_type/expires_at）。所有变更即时生效。
