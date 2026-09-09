// traffic_cache_test.go：流量统计缓存单测（TRAFFIC_ANALYSIS D1）。
// 覆盖：TTL 内命中（fn 只算一次）、TTL 过期重算、TTL=0 直通（每次真算）、
// 同 key 并发合并（singleflight）、计算失败不写缓存。
package obs

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrafficCacheHitAndExpiry(t *testing.T) {
	c := newTrafficCache()
	var calls atomic.Int64
	fn := func() (any, error) {
		calls.Add(1)
		return calls.Load(), nil
	}
	v1, cached1, err := c.do("k", time.Hour, fn)
	if err != nil || cached1 {
		t.Fatalf("首次计算：err=%v cached=%v，应未命中", err, cached1)
	}
	v2, cached2, _ := c.do("k", time.Hour, fn)
	if !cached2 || v1.(int64) != v2.(int64) {
		t.Fatalf("TTL 内应命中缓存且结果一致：cached=%v v1=%v v2=%v", cached2, v1, v2)
	}
	if calls.Load() != 1 {
		t.Fatalf("fn 应只执行 1 次，实际 %d", calls.Load())
	}
	// 过期：TTL 过后重算（用过去时刻写条目再读）
	c.entries["k2"] = trafficEntry{data: int64(99), expiresAt: time.Now().Add(-time.Second)}
	_, cachedK2, _ := c.do("k2", time.Millisecond, fn)
	if cachedK2 {
		t.Fatal("过期条目不应命中")
	}
}

func TestTrafficCacheDisabled(t *testing.T) {
	c := newTrafficCache()
	var calls atomic.Int64
	fn := func() (any, error) {
		calls.Add(1)
		return nil, nil
	}
	for i := 0; i < 3; i++ {
		_, cached, _ := c.do("k", 0, fn) // TTL=0：禁用直通
		if cached {
			t.Fatal("TTL=0 不应命中缓存")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("TTL=0 应每次真算，fn 实际执行 %d 次", calls.Load())
	}
}

func TestTrafficCacheSingleflight(t *testing.T) {
	c := newTrafficCache()
	release := make(chan struct{})
	var calls atomic.Int64
	fn := func() (any, error) {
		calls.Add(1)
		<-release // 阻塞在途计算，放大并发窗口
		return "ok", nil
	}
	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, cached, _ := c.do("k", time.Hour, fn)
			results[i] = cached
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // 等全部 goroutine 进 do()
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("并发同 key 应合并为 1 次计算，实际 %d", calls.Load())
	}
	for i, cached := range results {
		if cached { // 搭车方与计算方都算"未命中缓存"（本轮真算/搭车）
			t.Errorf("goroutine %d 不应报告缓存命中（在途合并非缓存）", i)
		}
	}
}

func TestTrafficCacheErrorNotCached(t *testing.T) {
	c := newTrafficCache()
	calls := 0
	fnErr := func() (any, error) { calls++; return nil, errFake }
	if _, _, err := c.do("k", time.Hour, fnErr); err == nil {
		t.Fatal("应返回计算错误")
	}
	fnOK := func() (any, error) { return "ok", nil }
	_, cached, _ := c.do("k", time.Hour, fnOK)
	if cached {
		t.Fatal("失败结果不应被缓存（下次应真算）")
	}
}

var errFake = &fakeError{}

type fakeError struct{}

func (*fakeError) Error() string { return "fake" }
