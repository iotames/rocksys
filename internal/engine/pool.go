package engine

import (
	"net"
	"net/http"
	"time"
)

// UpstreamPool 按 host 复用 HTTP transport 连接。
// 内部为单例 *http.Transport 已满足 MaxIdleConnsPerHost 语义：
// 同一 host 的多个空闲连接被复用，避免每次转发重新建连。
type UpstreamPool struct {
	transport *http.Transport
}

// MaxIdleConnsPerHost 每个上游 host 的最大空闲连接数。
const upstreamMaxIdleConnsPerHost = 10

// newUpstreamPool 创建上游连接池。
func newUpstreamPool() *UpstreamPool {
	return &UpstreamPool{
		transport: &http.Transport{
			MaxIdleConns:        100,                       // 全局最大空闲连接
			MaxIdleConnsPerHost: upstreamMaxIdleConnsPerHost, // 单 host 空闲连接上限
			IdleConnTimeout:     90 * time.Second,             // 空闲连接保留时长
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2: true,
		},
	}
}

// RoundTrip 执行一次上游 HTTP 往返。
func (p *UpstreamPool) RoundTrip(req *http.Request) (*http.Response, error) {
	return p.transport.RoundTrip(req)
}