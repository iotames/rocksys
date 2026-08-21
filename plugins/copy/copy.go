// Package copy 请求抄送挂件（L3 增强，默认关闭）。
//
// 把线上请求复制一份异步发送到 shadow 后端，用于流量审计 / 灰度影子验证。
// 借鉴 easywaf request-copy 能力，作为独立挂件挂 chain.Tail 槽位，
// 不改主架构（engine 转发逻辑零改动）。
//
// 配置项：COPY_TARGETS（字符串，逗号分隔的 shadow 后端 URL，空 = 关闭）
//
//	COPY_TARGETS=http://shadow-a:9100;http://shadow-b:9100
//
// 实现说明与限制：
//   - 转发完成后（OnResponse）从请求快照复制 method/URL/请求头，
//     异步发送到全部 shadow 后端；发送失败仅记录告警，不阻塞、不重试。
//   - 请求体不复制——engine 转发时请求体已被上游消费（OnResponse 时机无法重读 body），
//     如需 body 复制需在转发前缓存，属后续增强。
package copy

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// copyTargets 不可变目标快照。
type copyTargets struct {
	targets []string // shadow 后端 URL 列表（http(s)://host:port）
}

// Copy 请求抄送中间件（chain.Tail 槽位 + ResponseHook）。
// 运行态存于不可变快照，经 atomic.Value 原子替换，保证 Start 与在途 OnResponse 并发安全。
type Copy struct {
	cfg     conf.Manager
	targets string       // COPY_TARGETS 配置字符串（*string 注册，easyconf 自动写入）
	enabled bool         // *bool 注册：COPY_ENABLED
	snap    atomic.Value // 持有 *copyTargets 不可变快照
}

// 编译期断言：Copy 实现 hotswap.MiddlewareLifecycle 与 chain.ResponseHook。
var (
	_ hotswap.MiddlewareLifecycle = (*Copy)(nil)
	_ chain.ResponseHook          = (*Copy)(nil)
)

// New 创建抄送挂件并注册 COPY_TARGETS 配置项。
func New(cfgMgr conf.Manager) *Copy {
	c := &Copy{cfg: cfgMgr}
	c.snap.Store(&copyTargets{})
	if cfgMgr != nil {
		if err := cfgMgr.Register(&c.targets, "COPY_TARGETS", "",
			"请求抄送目标（逗号分隔的 shadow 后端 URL，空=关闭）",
			"示例：http://shadow-a:9100;http://shadow-b:9100"); err != nil {
			log.Warn("copy: 注册配置项失败", "name", "COPY_TARGETS", "err", err)
		}
		if err := cfgMgr.Register(&c.enabled, "COPY_ENABLED", "false", "是否启用请求抄送（false=不挂载）"); err != nil {
			log.Warn("copy: 注册配置项失败", "name", "COPY_ENABLED", "err", err)
		}
	}
	return c
}

// Name 返回中间件名称。
func (c *Copy) Name() string { return "copy" }

// Slot 挂载位置：Tail（响应阶段，配合 ResponseHook）。
func (c *Copy) Slot() chain.Slot { return chain.Tail }

// Handle 占位：响应处理全在 OnResponse，不参与转发前逻辑。
func (c *Copy) Handle(ctx *chain.Context) (next bool) { return false }

// Start 从配置解析并重建目标快照。解析失败时保留旧快照并返回 error。
func (c *Copy) Start(_ any) error {
	snap, err := parseTargets(c.targets)
	if err != nil {
		return err
	}
	c.snap.Store(snap)
	return nil
}

// Stop 清理资源（本挂件无特别资源）。
func (c *Copy) Stop() error { return nil }

// OnResponse 实现 chain.ResponseHook：复制请求快照，异步发送到全部 shadow 后端。
func (c *Copy) OnResponse(ctx *chain.Context) error {
	snap := c.snap.Load().(*copyTargets)
	if len(snap.targets) == 0 {
		return nil
	}
	// 同步拷贝请求快照（OnResponse 返回后 ctx.R 可能被复用，须拷贝后再异步使用）。
	req := cloneRequest(ctx.R)
	go func() {
		for _, t := range snap.targets {
			if err := sendCopy(t, req); err != nil {
				log.Warn("copy: 请求抄送失败", "target", t, "path", req.URL.Path, "err", err.Error())
			}
		}
	}()
	return nil
}

// parseTargets 解析 COPY_TARGETS：分号分隔的 URL 列表；空字符串 → 空目标（关闭）。
func parseTargets(s string) (*copyTargets, error) {
	snap := &copyTargets{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, "http://") && !strings.HasPrefix(part, "https://") {
			return nil, &parseErr{msg: "COPY_TARGETS 目标必须以 http(s):// 开头: " + part}
		}
		snap.targets = append(snap.targets, part)
	}
	return snap, nil
}

// parseErr 解析错误类型。
type parseErr struct{ msg string }

func (e *parseErr) Error() string { return e.msg }

// cloneRequest 构造无 body 的请求副本（method/URL/请求头）。
func cloneRequest(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	out := &http.Request{
		Method: r.Method,
		URL: &url.URL{
			Scheme:   r.URL.Scheme,
			Host:     r.URL.Host,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		},
		Header: r.Header.Clone(),
		Host:   r.Host,
	}
	return out
}

// sendCopy 发送单个抄送请求到 target（保留原始路径与查询串）。
func sendCopy(target string, req *http.Request) error {
	if req == nil {
		return nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	u.Path = req.URL.Path
	u.RawQuery = req.URL.RawQuery
	httpReq, err := http.NewRequest(req.Method, u.String(), nil)
	if err != nil {
		return err
	}
	httpReq.Header = req.Header
	if req.Host != "" {
		httpReq.Host = req.Host
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil
}
