# 路由分发接口（dispatch.md）

> 路由分发（三层体系：路由规则 → 负载均衡器 → 上游节点）管理接口契约。全部路径前缀 `/admin/dispatch/`，共 **19 个端点**。
> 通用约定（鉴权、`{"ok":true/false}` 响应、POST 副作用）见 [README.md](README.md) §1。

**组件门控**：所有端点在 `DISPATCH_ENABLED` 未装配或数据库未配置时返回 `503 {"ok":false,"error":"<三要素文案>"}`。

**统一行为**：

- 四表写操作（新增/更新/删除/恢复/标签）成功后**服务端自动重建内存路由快照**（热更免重启），无需手动重载；`/reload` 仅供多实例部署或外部直改数据库后手动同步；
- 规则/均衡器/节点删除均为**软删**（可恢复）；列表经 `include_deleted=1` 查看已删除行；
- 引用保护：均衡器存在未删规则引用、节点存在未删关系引用时删除返回 `409`（文案含引用数与去处）；
- 分页列表统一返回 `{"ok":true,"total":<总数>,"rows":[...]}`，响应头 `X-Total-Count` 同步总数。

## 1. 路由规则

### 1.1 `GET /admin/dispatch/rules` — 规则列表

查询参数：`limit`（1-10000，默认 50）、`offset`（≥0）、`keyword`（domain/path_value/title 模糊）、`path_type`（0=不限 / 1=前缀 / 2=精确 / 3=模式）、`enabled`（0=不限 / 1=仅启用）、`domain`（精确筛选）、`include_deleted`（0=仅活跃 / 1=仅已删除）。

行字段：`id, match_order, domain, path_type, path_value, title, upstream_id, enabled, remark, created_at, updated_at[, deleted_at]`。

### 1.2 `POST /admin/dispatch/rules` — 新增规则

```json
{"match_order":10,"domain":"a.com","path_type":1,"path_value":"/api","title":"示例","upstream_id":1,"enabled":true,"remark":"","tags":["灰度"]}
```

校验失败 400（序号 1-999、path_value 以 / 开头、均衡器须存在等）；保存成功后同序号已有其他规则时返回非阻断 `warnings` 数组（同序号按创建先后匹配）。

### 1.3 `POST /admin/dispatch/rules/update` — 整行更新

请求体同新增，`id` 必填；**整行语义**：启用开关与标签均随本次提交整组替换（列表行不含标签数据，故不设行内启停）。

### 1.4 `POST /admin/dispatch/rules/delete` — 软删规则

`{"id":1}`。软删可恢复；不做引用保护（规则是引用链顶端）。

### 1.5 `POST /admin/dispatch/rules/restore` — 恢复规则

`{"id":1}`。引用的均衡器已被软删（悬空）时 400 拒绝并提示先恢复均衡器。

### 1.6 `POST /admin/dispatch/rules/match-test` — 命中测试（只读）

`{"host":"a.com","path":"/api/x"}`（path 缺省 `/`）。只读无副作用：不动轮询游标、不计在途。返回：

- 未命中：`{"hit":false,"position":"default_upstream","message":"..."}`（走默认 upstream 兜底）；
- 命中：`{"hit":true,"rule":{...},"upstream":{...},"position":"<选点结果>","node":{...},"params":{...}}`；`position` 取值 `node`（选中节点转发）/ `upstream_disabled`（均衡器停用，实请求 503）/ `no_healthy_node`（无健康节点，实请求 503）。**所选节点为动态参考值**（随健康态/在途/游标变化），不要求与实请求一致。

### 1.7 `GET /admin/dispatch/rules/meta` — 枚举元数据

返回 `path_types` / `algos` / `priorities`（value/name/desc 三元组，表单下拉数据源）、`match_order`（min/max/step 与建议文案）、`domain`（填写规则说明）。

## 2. 快照重载

### 2.1 `POST /admin/dispatch/reload` — 手动重载

无请求体。拉全部四表重新构建并原子替换内存快照；校验失败保留旧快照并 500 返回原因。页内常规写操作已自动热更，本端点是多实例/外部改库场景的出口。

## 3. 负载均衡器

### 3.1 `GET /admin/dispatch/upstreams` — 均衡器列表

查询参数：`limit, offset, keyword（name 模糊）, enabled, include_deleted`。
行字段含节点关系组 `nodes:[{node_id,name,url,weight,priority,health}]`、`rule_refs`（被引用规则数）、`algo, sticky_enabled, sticky_cookie, enabled`。

### 3.2 `POST /admin/dispatch/upstreams` — 新增均衡器（含关系组）

```json
{"name":"订单池","algo":1,"sticky_enabled":true,"sticky_cookie":"rocksys_node","enabled":true,"remark":"","nodes":[{"node_id":1,"weight":1,"priority":0},{"node_id":2,"weight":2,"priority":0}]}
```

`algo`：1=round_robin / 2=least_conn；`priority`：0=高优 / 1=备份；`sticky_cookie` 缺省 `rocksys_node`。`name` 唯一（软删行除外），重复 400。关系组随表单**整组替换**保存。

### 3.3 `POST /admin/dispatch/upstreams/update` — 更新均衡器

请求体同新增，`id` 必填；关系组整组替换（实现为软删旧行 + 插入新行）。

### 3.4 `POST /admin/dispatch/upstreams/delete` — 软删均衡器

`{"id":1}`。存在未删规则引用时 `409`（文案含引用数与去处）。停用（enabled=false）不做引用保护，但引用它的规则命中后 503（fail-closed）。

### 3.5 `POST /admin/dispatch/upstreams/restore` — 恢复均衡器

`{"id":1}`。恢复时查重 `name`（活跃行已占用则 400）；原关系组随行一并恢复。

## 4. 上游节点

### 4.1 `GET /admin/dispatch/nodes` — 节点列表

查询参数：`limit, offset, keyword（name/url 模糊）, enabled, include_deleted`。
行字段：`id, name, url, hc_interval_ms, hc_timeout_ms, hc_path, enabled, remark, node_refs`（被引用数）。

### 4.2 `POST /admin/dispatch/nodes` — 新增节点

```json
{"name":"订单节点-1","url":"http://10.0.0.11:8080","hc_interval_ms":20000,"hc_timeout_ms":5000,"hc_path":"/healthz","enabled":true,"remark":""}
```

`url` 须 `http(s)://` 开头且活跃行唯一；`hc_path` 留空 = 不探活、始终视为健康（宕机不自动摘除）。

### 4.3 `POST /admin/dispatch/nodes/update` — 更新节点

请求体同新增，`id` 必填；探活参数变更即时生效（探活任务差量重建）。

### 4.4 `POST /admin/dispatch/nodes/delete` — 软删节点

`{"id":1}`。存在未删关系引用时 `409`（文案含引用的均衡器数与去处）。

### 4.5 `POST /admin/dispatch/nodes/restore` — 恢复节点

`{"id":1}`。恢复时查重 `url`。

## 5. 标签

### 5.1 `GET /admin/dispatch/tags` — 标签全量列表

无分页（下拉与 chip 数据源）；行字段 `id, name, rule_refs`。

### 5.2 `POST /admin/dispatch/tags` — 新建标签

`{"name":"灰度"}`。归一转小写、活跃行唯一，重复 400。

### 5.3 `POST /admin/dispatch/tags/update` — 重命名

`{"id":1,"name":"internal"}`。重命名一次对所有引用生效（标签实体化模型）。

### 5.4 `POST /admin/dispatch/tags/delete` — 删除标签

`{"id":1}`。同步软删其全部规则关系行；**不触发快照重建**（标签不进运行时快照）；无恢复端点（可同名重建）。

## 6. 健康快照

### 6.1 `GET /admin/dispatch/health` — 节点实时健康 + 快照状态

无参数。返回 `{"ok":true,"ready":<快照是否已构建>,"total":N,"rows":[{"id","name","url","enabled","health","inflight"}]}`。

- `health` 三态：`healthy`（绿）/ `unhealthy`（红）/ `unknown`（灰 = 未探活：未配探活路径或未被启用均衡器引用）；
- `inflight` 为该节点当前在途请求数（least_conn 依据）；
- 本端点读内存 registry，无 DB 写、无探活动作，可高频轮询。
