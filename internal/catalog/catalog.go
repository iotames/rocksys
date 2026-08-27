// Package catalog 组件/服务元数据目录（WebUI 概览/详情/配置等页面展示用）。
//
// 数据权威在此：前端不再硬编码组件/服务说明，经 GET /admin/meta 拉取并缓存。
// 描述文案需与 docs/COMPONENTS.md、docs/HTTP_DATAFLOW.md 保持语义一致。
package catalog

// Component 链中间件元数据。
type Component struct {
	Name       string `json:"name"`        // 组件英文名（路由/开关键）
	Title      string `json:"title"`       // 中文名
	Desc       string `json:"desc"`        // 用户视角说明（简明无歧义）
	Slot       string `json:"slot"`        // 链槽位：Head / Middle / Tail
	SlotLabel  string `json:"slot_label"`  // 环节展示名：入口环节 / 分发环节 / 响应环节
	EnabledKey string `json:"enabled_key"` // 自动开关配置键（XXX_ENABLED）
	Kind       string `json:"kind"`        // 恒为 middleware（链中间件）
}

// Service 独立服务元数据。
type Service struct {
	Name  string `json:"name"`  // 服务英文名
	Title string `json:"title"` // 中文名
	Desc  string `json:"desc"`  // 用户视角说明
	Kind  string `json:"kind"`  // 恒为 component（独立组件）
}

// DefaultComponents 返回 9 个链中间件元数据（与 HTTP_DATAFLOW.md 链路顺序一致）。
func DefaultComponents() []Component {
	return []Component{
		{Name: "shield", Title: "防护", Slot: "Head", SlotLabel: "入口环节", EnabledKey: "SHIELD_ENABLED", Kind: "middleware",
			Desc: "入口安全防护：按 IP 黑白名单、WAF 规则与限流拦截请求；命中返回 403/429 并中断链路，未命中放行。"},
		{Name: "trace", Title: "透传", Slot: "Head", SlotLabel: "入口环节", EnabledKey: "TRACE_ENABLED", Kind: "middleware",
			Desc: "链路追踪：将请求的 trace_id 写入响应头 X-Trace-Id，便于串联全链路日志（trace_id 由入口自动生成）。"},
		{Name: "auth", Title: "认证", Slot: "Head", SlotLabel: "入口环节", EnabledKey: "AUTH_ENABLED", Kind: "middleware",
			Desc: "JWT 鉴权：校验 Authorization 中的令牌（签名与有效期），合法放行并识别租户，非法返回 401。"},
		{Name: "dispatch", Title: "分发", Slot: "Middle", SlotLabel: "分发环节", EnabledKey: "DISPATCH_ENABLED", Kind: "middleware",
			Desc: "路由决策：按 URL 规则选出目标后端并写入转发信息，实际转发由转发引擎执行；未命中路由规则则进入 ROCKSYS_UPSTREAM 默认后端，命中但节点不可用返回 503。"},
		{Name: "rewrite", Title: "改写", Slot: "Middle", SlotLabel: "分发环节", EnabledKey: "REWRITE_ENABLED", Kind: "middleware",
			Desc: "转发前改写：按规则调整请求的 URI 前缀或注入请求头，随后由转发引擎转发。"},
		{Name: "script", Title: "脚本", Slot: "Middle", SlotLabel: "分发环节", EnabledKey: "SCRIPT_ENABLED", Kind: "middleware",
			Desc: "Lua 策略引擎：执行自定义脚本（单脚本限时 100ms），可改写目标/请求/响应，也可直接返回响应终止转发。"},
		{Name: "obs", Title: "观测", Slot: "Tail", SlotLabel: "响应环节", EnabledKey: "OBS_ENABLED", Kind: "middleware",
			Desc: "请求观测：记录访问日志（含分环节耗时）并聚合 QPS/延迟/错误率等指标，供概览与日志页查看。"},
		{Name: "copy", Title: "抄送", Slot: "Tail", SlotLabel: "响应环节", EnabledKey: "COPY_ENABLED", Kind: "middleware",
			Desc: "流量影子：转发完成后异步复制请求（不含请求体）到影子后端，不改写响应、不阻塞主链，失败仅告警。"},
		{Name: "result", Title: "结果", Slot: "Tail", SlotLabel: "响应环节", EnabledKey: "RESULT_ENABLED", Kind: "middleware",
			Desc: "出口加工：按规则对 JSON 响应脱敏或封装为统一格式（Envelope）；非 JSON 响应原样透传。"},
	}
}

// DefaultServices 返回 4 个独立服务元数据（消息组件按配置条件装配）。
func DefaultServices() []Service {
	return []Service{
		{Name: "config", Title: "配置服务", Kind: "component",
			Desc: "KV 配置服务：集中读写配置（默认本地文件），变更支持订阅广播，供各组件与服务使用。"},
		{Name: "registry", Title: "注册", Kind: "component",
			Desc: "服务注册与发现：实例注册、心跳续约、超时自动摘除，实例变更自动同步到分发（dispatch）路由。"},
		{Name: "object", Title: "存储", Kind: "component",
			Desc: "本地对象存储：对象读写存储于本地磁盘（含路径穿越防护）。"},
		{Name: "mq", Title: "消息", Kind: "component",
			Desc: "异步消息可靠投递：Outbox 模式（业务事务与消息同写）＋轮询投递，失败自动重试、超限转死信，不依赖独立 MQ。"},
	}
}
