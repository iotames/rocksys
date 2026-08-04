// Package script RockScript：Lua 策略引擎（转发链中间件，chain.Middle 槽位）。
//
// 依据 DEV_HANDBOOK.md 第 15 章实现：脚本热发布/回滚、Lua 预编译缓存、VM 池、
// 执行超时（默认 100ms）、Sandbox 白名单（禁止 os/io/file/net/ffi 模块）。
package script

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iotames/easyserver/log"
	lua "github.com/yuin/gopher-lua"

	"rocksys/internal/chain"
	"rocksys/internal/hotswap"
)

// 编译期断言：Engine 满足 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Engine)(nil)

// 默认执行超时（§15：默认 100ms，用 lua.ContextDeadline 等价机制控制，见 §4.6）。
const defaultTimeout = 100 * time.Millisecond

// forbiddenModules 沙箱禁止引用的模块（§15，含 require("os") / os.execute 等模式）。
var forbiddenModules = []string{"os", "io", "file", "net", "ffi"}

// ScriptVersion 编译缓存中的单个脚本版本（§15）。
//
// Proto 为 gopher-lua 预编译产物：发布（Publish）时一次性编译缓存，
// 运行期用 L.NewFunctionFromProto 还原为 Lua 函数执行，零编译成本。
type ScriptVersion struct {
	Name        string
	Source      string
	Version     int
	PublishedAt time.Time // 发布时刻（供 WebUI 版本时间线展示）
	Proto       *lua.FunctionProto
}

// ScriptInfo 脚本概要（供 WebUI /admin/script/list 输出）。
type ScriptInfo struct {
	Name          string        `json:"name"`
	CurrentVersion int          `json:"current_version"`
	Versions      []ScriptVerInfo `json:"versions"`
}

// ScriptVerInfo 单个历史版本概要（供 WebUI 版本时间线展示）。
type ScriptVerInfo struct {
	Version     int    `json:"version"`
	PublishedAt string `json:"published_at"` // RFC3339
}

// scriptsSnapshot 不可变脚本快照（§6.2/§15）：Publish/Rollback/Start 整体重建后原子替换，
// 在途请求的 Handle 持旧快照继续，保证并发安全。
type scriptsSnapshot struct {
	scripts map[string]*ScriptVersion          // 当前生效脚本（按名字）
	history map[string][]*ScriptVersion        // 各脚本全部发布历史（按版本升序），供回滚
	nextVer int                                // 版本号单调递增计数器
}

// newSnapshot 返回空快照，版本号从 1 开始。
func newSnapshot() *scriptsSnapshot {
	return &scriptsSnapshot{
		scripts: make(map[string]*ScriptVersion),
		history: make(map[string][]*ScriptVersion),
		nextVer: 1,
	}
}

// names 返回当前生效脚本名的有序切片（保证执行顺序确定性）。
func (s *scriptsSnapshot) names() []string {
	out := make([]string, 0, len(s.scripts))
	for name := range s.scripts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Engine RockScript：Lua 策略引擎（chain.Middle 槽位）。
//
// scripts 存于 atomic.Value 不可变快照；mu 仅保护 Publish/Rollback/Start 的
// 读改写序列（保证版本号单调唯一），Handle 只原子读快照、不持锁。
type Engine struct {
	scripts atomic.Value        // *scriptsSnapshot（不可变快照）
	vmPool  sync.Pool           // Lua VM 池（沙箱化、移除非安全全局函数）
	timeout time.Duration       // 执行超时，默认 100ms
	mu      sync.Mutex          // 序列化脚本快照的读改写
}

// New 创建脚本引擎。timeout<=0 时使用默认 100ms。
func New(timeout time.Duration) *Engine {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	e := &Engine{timeout: timeout}
	e.scripts.Store(newSnapshot())
	e.vmPool = sync.Pool{New: func() any { return e.newVM() }}
	return e
}

// Name 返回中间件名称。
func (e *Engine) Name() string { return "script" }

// Slot 挂载位置：路由分发后、转发前执行（§15）。
func (e *Engine) Slot() chain.Slot { return chain.Middle }

// Start 重建脚本快照并原子替换，保留已发布脚本（§6.3 流程 C）。
// 脚本由 Publish 管理，Start 不清空；此处整体重建以符合"不可变快照"约定。
func (e *Engine) Start(_ any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scripts.Store(cloneSnapshot(e.current()))
	return nil
}

// Stop 清理资源：清空脚本快照（§15）。
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scripts.Store(newSnapshot())
	return nil
}

// current 返回当前不可变快照。
func (e *Engine) current() *scriptsSnapshot {
	if v := e.scripts.Load(); v != nil {
		return v.(*scriptsSnapshot)
	}
	return newSnapshot()
}

// Publish 发布并生效一个脚本：沙箱检查 → 预编译 → 存入编译缓存，返回单调递增版本号。
// 沙箱拒绝（引用 os/io/file/net/ffi）或编译失败时返回 error，发布失败（§15 验收）。
func (e *Engine) Publish(name, source string) (int, error) {
	if err := checkSandbox(source); err != nil {
		return 0, err
	}
	proto, err := compileScript(name, source)
	if err != nil {
		return 0, fmt.Errorf("script: 编译失败 %q: %w", name, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ns := cloneSnapshot(e.current())
	ver := ns.nextVer
	ns.nextVer++
	sv := &ScriptVersion{Name: name, Source: source, Version: ver, PublishedAt: time.Now(), Proto: proto}
	ns.scripts[name] = sv
	ns.history[name] = append(ns.history[name], sv)
	e.scripts.Store(ns)
	return ver, nil
}

// Rollback 回滚脚本到指定版本；version<=0 时移除该脚本（§15 验收）。
// 仅能回滚到已发布的历史版本，错误返回 error。
func (e *Engine) Rollback(name string, version int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.current()
	if version <= 0 {
		if _, ok := cur.scripts[name]; !ok {
			return fmt.Errorf("script: 未找到脚本 %q", name)
		}
		ns := cloneSnapshot(cur)
		delete(ns.scripts, name)
		delete(ns.history, name)
		e.scripts.Store(ns)
		return nil
	}
	hist, ok := cur.history[name]
	if !ok {
		return fmt.Errorf("script: 未找到脚本 %q 的发布历史", name)
	}
	for _, sv := range hist {
		if sv.Version == version {
			ns := cloneSnapshot(cur)
			ns.scripts[name] = sv
			e.scripts.Store(ns)
			return nil
		}
	}
	return fmt.Errorf("script: 未找到脚本 %q 的版本 %d", name, version)
}

// ListScripts 返回全部已发布脚本概要（含版本历史，按版本升序）。
// 仅供管理接口 /admin/script/list 输出；脚本为内存态，进程重启后为空。
func (e *Engine) ListScripts() []ScriptInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.current()
	names := cur.names()
	out := make([]ScriptInfo, 0, len(names))
	for _, name := range names {
		sv := cur.scripts[name]
		info := ScriptInfo{
			Name:           name,
			CurrentVersion: sv.Version,
			Versions:       make([]ScriptVerInfo, 0, len(cur.history[name])),
		}
		for _, v := range cur.history[name] {
			info.Versions = append(info.Versions, ScriptVerInfo{
				Version:     v.Version,
				PublishedAt: v.PublishedAt.Format(time.RFC3339),
			})
		}
		out = append(out, info)
	}
	return out
}

// Handle 每请求执行全部已发布脚本（§15：默认执行全部脚本，脚本内用 req.path() 等自行判断）。
// 任一脚本调用 ctx.respond 并已写响应 → 返回 false 中断链；否则返回 true 继续转发。
func (e *Engine) Handle(ctx *chain.Context) (next bool) {
	snap := e.current()
	if snap == nil || len(snap.scripts) == 0 {
		return true
	}
	L := e.acquireVM()
	defer e.releaseVM(L)

	var responded bool
	installAPI(L, ctx, &responded)

	// 执行超时（默认 100ms）：经 L.SetContext 让 VM 主循环按指令周期检查 deadline，
	// 穷举死循环也会被中断（§4.6）。RemoveContext 恢复普通主循环，便于 VM 复用。
	tctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	L.SetContext(tctx)
	defer L.RemoveContext()

	for _, name := range snap.names() {
		if responded {
			break
		}
		if err := runOne(L, snap.scripts[name]); err != nil {
			log.Warn("script: 脚本执行失败", "name", name, "err", err)
			if responded {
				break
			}
		}
	}
	cleanupGlobals(L)
	return !responded
}

// acquireVM 从池中取一个沙箱化 Lua VM。
func (e *Engine) acquireVM() *lua.LState {
	return e.vmPool.Get().(*lua.LState)
}

// releaseVM 归还 VM 到池中复用。
func (e *Engine) releaseVM(L *lua.LState) {
	e.vmPool.Put(L)
}

// runOne 执行预编译脚本：LoadProto 还原为 Lua 包并调用。
func runOne(L *lua.LState, sv *ScriptVersion) error {
	L.Push(L.NewFunctionFromProto(sv.Proto))
	return L.PCall(0, lua.MultRet, nil)
}

// cloneSnapshot 深拷贝快照（scripts 引用共享只读 ScriptVersion，history 切片需复制防误改）。
func cloneSnapshot(src *scriptsSnapshot) *scriptsSnapshot {
	ns := newSnapshot()
	ns.nextVer = src.nextVer
	for name, sv := range src.scripts {
		ns.scripts[name] = sv
	}
	for name, hist := range src.history {
		ns.history[name] = append([]*ScriptVersion(nil), hist...)
	}
	return ns
}