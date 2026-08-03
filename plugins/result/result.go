// Package result L3 结果处理（转发链中间件）。
package result

import (
	"encoding/json"
	"strings"
	"sync/atomic"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// Envelope 统一响应封装（§11.1）。
type Envelope struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// snapshot 运行态不可变快照（§6.3 原子快照语义）。
type snapshot struct {
	maskFields []string // 需要脱敏的 JSON 字段名
	wrap       bool     // 是否统一封装 Envelope
}

// Result L3 结果处理中间件：挂 chain.Tail 槽位，响应阶段脱敏/统一封装。
type Result struct {
	cfg        conf.Manager
	maskFields string         // 逗号分隔的脱敏字段（*string 注册）
	wrap       bool           // 是否统一封装（*bool 注册）
	snapshot   atomic.Value   // 运行态快照（*snapshot）
}

// 编译期断言：Result 实现 hotswap.MiddlewareLifecycle，可被 hotswap 管理。
var _ hotswap.MiddlewareLifecycle = (*Result)(nil)

// New 创建 result 挂件并注册自身配置项。
// ★ 不注册 RESULT_ENABLED：启用/禁用由 hotswap 开关管理。
func New(cfgMgr conf.Manager) *Result {
	r := &Result{cfg: cfgMgr}
	_ = cfgMgr.Register(&r.maskFields, "RESULT_MASK_FIELDS", "", "脱敏字段（逗号分隔，如 phone,id_card,token）")
	_ = cfgMgr.Register(&r.wrap, "RESULT_WRAP", "false", "是否统一封装为 {code,msg,data}")
	r.snapshot.Store(&snapshot{})
	return r
}

// Name 中间件名（hotswap 按此名启停）。
func (r *Result) Name() string { return "result" }

// Slot 挂载位置：L3 结果处理在响应阶段执行。
func (r *Result) Slot() chain.Slot { return chain.Tail }

// Handle 占位：响应处理全在 OnResponse（§4.6），不参与转发前逻辑。
func (r *Result) Handle(ctx *chain.Context) (next bool) { return false }

// Start 用当前配置重建运行态快照（热更/Enable 时调用）。
func (r *Result) Start(cfg any) error {
	snap := &snapshot{wrap: r.wrap}
	if s := strings.TrimSpace(r.maskFields); s != "" {
		for _, f := range strings.Split(s, ",") {
			if f = strings.TrimSpace(f); f != "" {
				snap.maskFields = append(snap.maskFields, f)
			}
		}
	}
	r.snapshot.Store(snap)
	return nil
}

// Stop 清理资源：本挂件无外部资源，直接返回 nil。
func (r *Result) Stop() error { return nil }

// OnResponse 实现 chain.ResponseHook（§11.2）：
// JSON 响应 → 可选脱敏 → 可选 Wrap 成 Envelope → WriteFinal；非 JSON 原样透传。
func (r *Result) OnResponse(ctx *chain.Context) error {
	snap := r.snapshot.Load().(*snapshot)

	// 非 JSON 原样透传：不报错、不修改、不调 WriteFinal（由 Adapter 回写缓冲）。
	if !strings.Contains(ctx.RespHeader.Get("Content-Type"), "application/json") {
		return nil
	}

	var data any
	if err := json.Unmarshal(ctx.RespBody, &data); err != nil {
		log.Warn("result: json unmarshal 失败，原样透传", "err", err)
		return nil
	}

	// 可选：脱敏指定字段。
	if len(snap.maskFields) > 0 {
		maskJSON(data, snap.maskFields)
	}

	// 可选：统一封装 Envelope（code/msg 从上游 JSON 自动取，缺省 0/"ok"）。
	if snap.wrap {
		code, msg := extractCodeMsg(data)
		data = Envelope{Code: code, Msg: msg, Data: data}
	}

	newBody, err := json.Marshal(data)
	if err != nil {
		log.Warn("result: json marshal 失败，原样透传", "err", err)
		return nil
	}

	// ★ header 用 ctx.RespHeader（上游响应头，已含 application/json），不覆盖 Content-Type。
	return ctx.WriteFinal(200, ctx.RespHeader, newBody)
}

// extractCodeMsg 从上游 JSON 对象自动提取 code/msg（缺省 0/"ok"）。
func extractCodeMsg(data any) (code int, msg string) {
	code, msg = 0, "ok"
	m, ok := data.(map[string]any)
	if !ok {
		return code, msg
	}
	if v, ok := m["code"]; ok {
		if f, ok := v.(float64); ok {
			code = int(f)
		}
	}
	if v, ok := m["msg"]; ok {
		if s, ok := v.(string); ok {
			msg = s
		}
	}
	return code, msg
}

// maskJSON 递归遍历 JSON 结构，对指定字段做脱敏（仅处理字符串值）。
func maskJSON(v any, fields []string) {
	switch t := v.(type) {
	case map[string]any:
		for _, f := range fields {
			if s, ok := t[f].(string); ok {
				t[f] = mask(s)
			}
		}
		for _, val := range t {
			maskJSON(val, fields)
		}
	case []any:
		for _, val := range t {
			maskJSON(val, fields)
		}
	}
}

// mask 部分替换脱敏：长度 >7 保留前 3 后 4（138****1234 风格），否则全 ****。
func mask(value string) string {
	r := []rune(value)
	if len(r) <= 7 {
		return "****"
	}
	return string(r[:3]) + "****" + string(r[len(r)-4:])
}
