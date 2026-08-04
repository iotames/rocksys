// 主动健康检查：按 interval 周期探测节点，2xx/3xx 判健康，网络错误/超时/其他状态码判不健康。
//
// 生命周期绑定路由表：Dispatch.Start 启动探活 goroutine（随新表），Stop/热更时旧表停止并等待退出，
// 避免 goroutine 泄漏。全部节点不健康时，Select 返回 ok=false，由 Handle 写 503。
package dispatch

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/iotames/easyserver/log"
)

// HealthCheck 主动健康检查配置与运行态。
type HealthCheck struct {
	Interval time.Duration // 探活周期
	Timeout  time.Duration // 单次探测超时
	Path     string        // 探测路径（如 /healthz）

	client   *http.Client
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// startHealthChecks 启动路由表内所有配置了健康检查的规则探活 goroutine。
func (rt *RouteTable) startHealthChecks() {
	for _, r := range rt.rules {
		if r.HealthCheck == nil {
			continue
		}
		hc := r.HealthCheck
		if hc.client == nil {
			hc.client = &http.Client{}
		}
		hc.stop = make(chan struct{})
		hc.done = make(chan struct{})
		go hc.run(r)
	}
}

// stopHealthChecks 停止路由表内所有健康检查 goroutine（阻塞等待退出）。
// 幂等：未启动过的规则直接跳过。
func (rt *RouteTable) stopHealthChecks() {
	for _, r := range rt.rules {
		if r.HealthCheck == nil {
			continue
		}
		r.HealthCheck.stopHealthCheck()
	}
}

// stopHealthCheck 停止单个健康检查（幂等：仅首个调用者生效并等待 goroutine 退出）。
func (hc *HealthCheck) stopHealthCheck() {
	hc.stopOnce.Do(func() {
		if hc.stop != nil {
			close(hc.stop)
			<-hc.done
		}
	})
}

// run 探活循环：启动即探一次（避免窗口期流量全打向坏节点），随后按 Interval 轮询。
func (hc *HealthCheck) run(r *Rule) {
	defer close(hc.done)
	ticker := time.NewTicker(hc.Interval)
	defer ticker.Stop()

	hc.probeAll(r)
	for {
		select {
		case <-hc.stop:
			return
		case <-ticker.C:
			hc.probeAll(r)
		}
	}
}

// probeAll 探测该规则全部节点。
func (hc *HealthCheck) probeAll(r *Rule) {
	for _, n := range r.Nodes {
		hc.probe(n)
	}
}

// probe 探测单个节点：2xx/3xx → 健康；其余 → 不健康。
func (hc *HealthCheck) probe(n *Node) {
	ctx, cancel := context.WithTimeout(context.Background(), hc.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.URL+hc.Path, nil)
	if err != nil {
		n.healthy.Store(false)
		return
	}
	resp, err := hc.client.Do(req)
	if err != nil {
		n.healthy.Store(false)
		return
	}
	_ = resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	n.healthy.Store(ok)
	if !ok {
		log.Warn("dispatch: health probe failed", "url", n.URL, "path", hc.Path, "status", resp.StatusCode)
	}
}
