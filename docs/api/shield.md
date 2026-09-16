# Admin API · shield 管理端点组（WAF 统计 + 动态黑白名单）

### 3.18 shield 管理端点组 — WAF 监控统计 + 动态黑白名单

**WAF 监控统计**（metrics/events/stats/prune，实现见 `plugins/shield/admin.go`）：

| 端点 | 说明 |
|------|------|
| `GET /admin/shield/metrics` | 实时拦截计数（内存滑动窗口，DB 未配置也可用）；query `window=1m\|5m\|15m\|1h`（缺省 1m 兼容现状；内存窗口固定 1 小时 = 60×1 分钟桶，按所选窗口由整分钟桶聚合、零误差）；响应 `{window,window_minutes,window_seconds,total,by_type,written,dropped}`，`written`/`dropped` 为本次运行累计落库/丢弃条数（重启清零） |
| `GET /admin/shield/total` | 拦截事件**落库总数**（查库 `COUNT` 全范围，受保留期影响，清理会减少）；与 metrics 内存窗口口径不同；响应 `{"total":N}`；DB 未配置（记录器未启用）503。前端进页查一次、不跟随窗口刷新 |
| `GET /admin/shield/events` | 拦截明细（JSONL）；query：`from`/`to`（日期或分钟精度）、`block_type`（1-10）、`client_ip`、`limit`（1-10000，缺省 500）、`offset`；总数经 `X-Total-Count` 头回传；每行 JSON 额外携带 `in_blacklist` 字段（bool，该行 IP 是否命中当前生效黑名单，内存快照判定与 stats TOP 同源，供前端「IP封禁」按钮置灰）；行含 `user_agent` 与 geo 关联维度 `country_code`/`country_name`/`province`/`city`（GEOIP_LIST 方案：`LEFT JOIN geoip_list` 关联，未命中回退实时解析；全部失败为空串，前端显示「未知」） |
| `GET /admin/shield/stats` | 聚合统计；响应 `{days,total,daily:[{day,block_type,cnt,type_name}],top_ips:[{client_ip,cnt,in_blacklist,country,city}],blacklist_addable}`（`in_blacklist`=该 IP 是否命中当前生效黑名单，与拦截判定同源；`blacklist_addable`=DB 黑名单是否可用，WebUI 据此显示勾选列与批量加黑按钮；Top IP 行 `country`/`city` 为 **geo 查询时逐行解析**（行数少免加列），GeoIP 未加载时不下发该两字段，前端显示占位「—」） |
| `POST /admin/shield/prune` | 手动清理拦截明细；body `{"days":N}`（0-3650，缺省用配置默认值）；响应 `{"ok":true,"deleted":N}` |

**动态黑白名单**（WAF 方案 DB 化，DB 未配置时端点统一 503；副作用一律 POST）：

| 端点 | 方法 / 语义 |
|------|------|
| `/admin/shield/blacklist` | GET 列表（query：`ip` 模糊、`block_type`、`valid_only`、`limit`、`offset`、`sort`；响应 `{total,rows}`）；POST 新增（body `{"ip","title","block_type","expires_at"}`，expires_at 为 RFC3339 可空）。`sort` 服务端排序（黑名单专属）：白名单映射 `hit_count`/`warn_times`/`created_at`/`expires_at`/`updated_at`/`block_type` → 对应列 **DESC**（固定倒序），非法/缺省回默认 `id DESC`（最近添加在前）；字符串字段（ip/title）不参与排序 |
| `/admin/shield/blacklist/update` | POST 更新（body `{"id","title","block_type","expires_at"}`） |
| `/admin/shield/blacklist/delete` | POST 软删（body `{"id"}`；可恢复） |
| `/admin/shield/blacklist/restore` | POST 恢复软删（body `{"id"}`） |
| `/admin/shield/blacklist/import` | POST 批量导入：**body 为纯文本**（每行一个精确 IP/CIDR，兼容外挂文件格式；兼容 JSON 字符串编码），query 可选 `title`/`block_type`；响应 `{"ok":true,"imported":N,"skipped":N}` |
| `/admin/shield/blacklist/sync_file` | POST 从外挂规则文件 `HOT_SCRIPTS_DIR/rules/ip_blacklist.txt` 同步 IP 入库（外挂优先、内嵌兜底；`#` 注释/空行忽略），固定 `title="来自 ip_blacklist.txt 同步"`、`block_type=11` 人工收录；幂等（重复同步 skipped 递增）；响应 `{"ok":true,"imported":N,"skipped":N}`；文件缺失/为空/无有效行 → 400 文本（三要素文案：发生了什么 + 原因 + 检查 `hotscripts/rules/ip_blacklist.txt` 后重试） |
| `/admin/shield/blacklist/ban` | POST 专用封禁端点：body `{"ip","title","block_type","duration"}`（`block_type` 1-11 缺省 11 人工收录；`duration`=`"24h"`（缺省，服务端换算 now+24h）\|`"permanent"`→expires_at=NULL；title 空 → `"人工封禁"`）。三态：① 无记录 → 新增入库 `warn_times=1`；② 活跃条目 → 400「已在黑名单」+ 前往黑名单列表管理指引；③ 软删/过期条目 → 恢复（清 deleted_at）+ 按所选时长落 expires_at + `warn_times`+1；**累计满 5 次的限时封禁自动转永久**（本就永久的条目恢复后仍永久）。成功 `{"ok":true,"to_permanent":bool}`（`to_permanent`=本次已由限时转永久）；写库成功即重建拦截快照 |
| `/admin/shield/jail` | GET 小黑屋：当前在押的全部封禁条目（未过期、未软删；永久封禁 `expires_at` 为 NULL 视为不过期一并收录），限时封禁按解封时间升序（临近解封在前）、永久封禁殿后；query `limit` 默认 20、上限 100（非法/越界回默认）；响应 `{"total":N,"rows":[{ip,block_type,hit_count,warn_times,created_at,expires_at}]}`；DB 未配置 503 |
| `/admin/shield/whitelist` 及 update/delete/restore/import | 白名单同构（无 block_type/expires_at 字段） |

> 全部变更写库后**即时重建拦截快照**（不等 TTL 兜底）；数据表见 `docs/DATA_DICT.md` §2.5-2.7；外挂 `rules/ip_blacklist.txt` 存量条目可直接粘贴本接口批量导入。

