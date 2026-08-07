package chain

import (
	"errors"
	"net/http"
	"runtime/debug"
	"sync"

	"github.com/iotames/easyserver/log"
)

// Chain 中间件链：三段槽位各自持有不可变快照，读写均持锁保护。
type Chain struct {
	segments [3][]Middleware // 索引 = Slot，不可变快照
	mu       sync.RWMutex
}

// New 创建空转发链。
func New() *Chain {
	return &Chain{}
}

// Add 添加中间件到指定槽位末尾。
func (c *Chain) Add(slot Slot, m Middleware) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.segments[slot] = append(c.segments[slot], m)
}

// Remove 按名称从所有槽位中移除。
func (c *Chain) Remove(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	found := false
	for i := range c.segments {
		list := c.segments[i]
		kept := list[:0]
		for _, m := range list {
			if m.Name() == name {
				found = true
				continue
			}
			kept = append(kept, m)
		}
		c.segments[i] = kept
	}
	if !found {
		return errors.New("middleware not found: " + name)
	}
	return nil
}

// Replace 原子替换整个槽位（用于热切换）；newList 可以为 nil（等效清空该槽位）。
func (c *Chain) Replace(slot Slot, newList []Middleware) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.segments[slot] = newList
}

// safeHandle 包装单个中间件执行：panic 时记录中间件名 + 完整堆栈，尝试写 500，返回 false（中断链）。
// 写 500 用 http.Error：主流场景（panic 时未写响应）正常写 500；若中间件违规已写过响应
// （违反 interface.go 契约——返回 true 的中间件禁止写响应），net/http 状态码不覆盖、
// body 可能追加，属退化行为但不崩溃。recover 只兜底不吞错：完整堆栈必入日志。
func safeHandle(m Middleware, ctx *Context) (next bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("chain: middleware panic recovered", "name", m.Name(), "panic", r, "stack", string(debug.Stack()))
			http.Error(ctx.W, "internal server error", http.StatusInternalServerError)
			next = false
		}
	}()
	return m.Handle(ctx)
}

// Execute 执行转发前链（仅 Head → Middle，不执行 Tail）。
// 任一返回 false 则中断（中间件已自行响应或 panic 被 safeHandle 兜底为 500）；全部返回 true 则 engine 执行 Forward。
func (c *Chain) Execute(ctx *Context) (shouldForward bool) {
	c.mu.RLock()
	head := c.segments[Head]
	middle := c.segments[Middle]
	c.mu.RUnlock()

	for _, m := range head {
		if !safeHandle(m, ctx) {
			return false
		}
	}
	for _, m := range middle {
		if !safeHandle(m, ctx) {
			return false
		}
	}
	return true
}

// HasResponseHook 判断指定槽位是否存在实现了 ResponseHook 的中间件。
func (c *Chain) HasResponseHook(slot Slot) bool {
	return len(c.ResponseHooks(slot)) > 0
}

// ResponseHooks 返回指定槽位中实现了 ResponseHook 的中间件，
// 顺序 = 注册顺序的逆序（后注册的在切片前面），调用方正向 for range 即为逆序执行。
func (c *Chain) ResponseHooks(slot Slot) []ResponseHook {
	c.mu.RLock()
	list := c.segments[slot]
	c.mu.RUnlock()

	var hooks []ResponseHook
	for i := len(list) - 1; i >= 0; i-- {
		if h, ok := list[i].(ResponseHook); ok {
			hooks = append(hooks, h)
		}
	}
	return hooks
}

// WriteFinal 由 Tail 中间件调用：写入最终响应并置 done=true。
// 若已有中间件写过（done=true）则返回 error；响应头须在调用前设置完。
// ★ 移除可能过期的 Content-Length：Tail 中间件（如 result）改写 body 后，
//
//	上游响应头里的 Content-Length 已不匹配，须让 Go 按实际 body 重新计算，否则连接被截断。
func (c *Context) WriteFinal(code int, header http.Header, body []byte) error {
	if c.done {
		return errors.New("final response already written")
	}
	c.done = true

	if header != nil {
		copyHeader(c.RespW.Header(), header)
	}
	c.RespW.Header().Del("Content-Length")
	c.RespW.WriteHeader(code)
	c.RespW.Write(body)
	return nil
}

// copyHeader 将 src 响应头复制到 dst（覆盖同名 key）。
func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
