// atomicPtr *Progress 原子指针（D25 快照替换：业务整体替换、中心原子读）。
// 标准库 atomic.Pointer[T] 泛型即可，独立小封装仅为集中注释语义。
package taskcenter

import (
	"sync/atomic"
)

type atomicPtr struct {
	v atomic.Pointer[Progress]
}

func (a *atomicPtr) store(p *Progress) { a.v.Store(p) }

func (a *atomicPtr) load() *Progress { return a.v.Load() }
