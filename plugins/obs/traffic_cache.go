// traffic_cache.go：流量统计结果缓存（TRAFFIC_ANALYSIS D1）。
//
// 纯标准库 singleflight（mutex+map）+ 过期时刻表：同 key 并发请求合并为一次计算，
// 命中未过期条目直接返回（computed_at 不变）。key=端点+from+to+bucket/source；
// 预设时间范围有限、自定义范围低频，条目量实际有界，首版不设上限与淘汰（已知边界，见 PLAN §3.4）。
package obs

import (
	"sync"
	"time"
)

// trafficEntry 一条缓存：结果 + 过期时刻。
type trafficEntry struct {
	data      any
	expiresAt time.Time
}

// trafficCall 同 key 并发合并的一次在途计算。
type trafficCall struct {
	wg   sync.WaitGroup
	data any
	err  error
}

// trafficCache 统计结果缓存（并发安全；TTL 动态读取，0=禁用缓存直通计算）。
type trafficCache struct {
	mu      sync.Mutex
	entries map[string]trafficEntry
	calls   map[string]*trafficCall // 在途计算（singleflight）
}

func newTrafficCache() *trafficCache {
	return &trafficCache{
		entries: map[string]trafficEntry{},
		calls:   map[string]*trafficCall{},
	}
}

// do 计算 or 命中缓存。返回值 data 为结果（调用方只读，不得修改）；cached 表示是否缓存命中。
// ttl 由调用方按当前配置动态传入（0 = 禁用：既不读也不写缓存，每次真算）。
func (c *trafficCache) do(key string, ttl time.Duration, fn func() (any, error)) (data any, cached bool, err error) {
	if ttl <= 0 {
		data, err = fn()
		return data, false, err
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && e.expiresAt.After(now) {
		c.mu.Unlock()
		return e.data, true, nil
	}
	if call, ok := c.calls[key]; ok { // 同 key 在途：搭车等待，合并为一次计算
		c.mu.Unlock()
		call.wg.Wait()
		return call.data, false, call.err
	}
	call := &trafficCall{}
	call.wg.Add(1)
	c.calls[key] = call
	c.mu.Unlock()

	call.data, call.err = fn()
	call.wg.Done()

	c.mu.Lock()
	delete(c.calls, key)
	if call.err == nil {
		c.entries[key] = trafficEntry{data: call.data, expiresAt: time.Now().Add(ttl)}
	}
	c.mu.Unlock()
	return call.data, false, call.err
}
