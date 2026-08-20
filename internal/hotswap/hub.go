// 外挂文件统一热更中枢 ScriptHub（统一内容中枢：缓存 + 监控 + 推送全部内聚）。
//
// 背景：三类外挂运行时读取文件（sql/、rules/、trusted_proxies/）的历史热更语义不一致——
// SQL 每次实时读磁盘（请求路径有文件 I/O）、WAF 规则需借配置热更/开关触发 Start 才重读、
// 可信代理改文件需重启。本中枢在底层唯一读取入口 ScriptDir.GetScriptBytes 之上新增一层，
// 把"内容如何生产"（监控 → 缓存 → 推送）整体收敛进一层，消费端只认识两个接口，感知机制彻底统一：
//
//   - GetScriptText(sub, relPath)：取内容。命中统一缓存纳秒级返回（请求路径零文件 I/O）；
//     未命中才走底层 ScriptDir.GetScriptBytes 并写入缓存。语义等价于现有 ScriptDir.GetScriptText，
//     消费端无感迁移。
//   - Subscribe(sub, fn)：订阅子目录。该子目录下有文件增/删/改 → fn(relPath) 被调用
//     （变化已重读入缓存，fn 内再 GetScriptText 即拿到新内容）。
//
// 统一后的热更语义（三类完全一致，SQL 例外见下）：
//
//	外挂文件变更 → ≤ HOT_FILES_WATCH_INTERVAL（默认 3s，easyconf 可配）内自动生效
//	             → 免重启、免借配置热更、免手动开关组件
//
// 内部职责（对消费端不可见）：
//  1. 统一缓存：map[子目录/相对路径]内容，sql/rules/trusted_proxies 同一池子；
//  2. 统一监控：指纹轮询（mtime 纳秒 + size，够捕获增/删/改、避免读盘比内容），
//     目录树递归（sql/ 下 mysql/postgres/sqlite 三层），文件增/删/改均触发；
//     hotscripts 不存在时指纹集合为空、监控永不触发（生产默认零额外开销）；
//  3. 统一推送：变化 → 重读 → 更新缓存 → 才通知订阅者（回调在独立 goroutine 执行，
//     不阻塞监控循环）。读失败保留旧内容、仅告警、不通知
//     （与 shield.Start 失败保留旧快照同语义：服务不中断）。
//
// 消费差异保留（本质差异不统一）：SQL 文本即用零订阅（吃统一缓存）、
// WAF 规则订阅后编译不可变快照（复用 Start(nil) 重建）、可信代理订阅后解析原子替换
// （atomic.Pointer）。差异只在"拿到文本后怎么消费"，生产链路完全一致。
//
// 底层红线不变：仍统一经 ScriptDir.GetScriptBytes 读取（外挂优先、内嵌兜底），签名与语义未动。
//
// 装配约定（见 cmd/rocksys/main.go buildServer）：
// NewScriptHub → 各消费端 Register/Subscribe（db.Open / shield.New / netutil.SubscribeHub）
// → 全部子目录注册完成后 Start()（幂等）；监控循环随 Manager.Shutdown 停止（mgr.SetScriptHub）。
package hotswap

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iotames/easyserver/log"
)

// ScriptHub 统一内容中枢：缓存 + 监控 + 推送全部内聚。
// 零值不可用，须经 NewScriptHub 构造。
type ScriptHub struct {
	interval time.Duration

	mu    sync.RWMutex
	dirs  map[string]*ScriptDir             // 子目录名 → ScriptDir（注册的消费端）
	cache map[string]string                 // sub/relPath → 内容（统一缓存池）
	fp    map[string]fileFP                 // sub/relPath → 指纹（监控比对基线）
	subs  map[string][]func(relPath string) // 子目录名 → 订阅者

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	wg        sync.WaitGroup
}

// fileFP 文件指纹：mtime（纳秒）+ size，够捕获增/删/改；不比较内容（避免读盘）。
type fileFP struct {
	size  int64
	mtime int64
}

// NewScriptHub 构造统一内容中枢。interval 为监控轮询间隔（≤0 回落默认 3s）。
func NewScriptHub(interval time.Duration) *ScriptHub {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	return &ScriptHub{
		interval: interval,
		dirs:     make(map[string]*ScriptDir),
		cache:    make(map[string]string),
		fp:       make(map[string]fileFP),
		subs:     make(map[string][]func(string)),
		stopCh:   make(chan struct{}),
	}
}

// Register 注册外挂子目录（sub 为业务固定子目录名：sql/rules/trusted_proxies）。
// 同一子目录重复注册返回错误（装配方重复注册是装配缺陷，尽早暴露）。
func (h *ScriptHub) Register(sub string, sd *ScriptDir) error {
	if sub == "" {
		return fmt.Errorf("hotswap: ScriptHub.Register 子目录名不能为空")
	}
	if sd == nil {
		return fmt.Errorf("hotswap: ScriptHub.Register(%q) ScriptDir 为 nil", sub)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.dirs[sub]; ok {
		return fmt.Errorf("hotswap: ScriptHub 子目录 %q 已注册", sub)
	}
	h.dirs[sub] = sd
	return nil
}

// GetScriptText 读取 sub 子目录下 relPath 的脚本文本（永远是最新）。
// 命中统一缓存直接返回（纳秒级，请求路径零文件 I/O）；未命中才走底层
// ScriptDir.GetScriptBytes（外挂优先、内嵌兜底）并写入缓存。
//
// 锁策略：查缓存与查 dirs 分开两次 RLock，期间不持锁调用 GetScriptBytes——
// 首次读取可能触发磁盘 I/O，持锁会阻塞并发 GetScriptText 的缓存命中路径。
// 子目录注册发生在装配期（运行期稳定），未命中注册的子目录属装配错误，直接报错。
func (h *ScriptHub) GetScriptText(sub, relPath string) (string, error) {
	relPath = filepath.ToSlash(relPath)
	if relPath == "" {
		return "", fmt.Errorf("hotswap: ScriptHub.GetScriptText(%q) 路径不能为空", sub)
	}
	key := subKey(sub, relPath)

	h.mu.RLock()
	if v, ok := h.cache[key]; ok {
		h.mu.RUnlock()
		return v, nil
	}
	h.mu.RUnlock()

	h.mu.RLock()
	sd, ok := h.dirs[sub]
	h.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("hotswap: ScriptHub 未注册子目录 %q", sub)
	}

	b, err := sd.GetScriptBytes(relPath)
	if err != nil {
		return "", err
	}
	text := string(b)

	h.mu.Lock()
	h.cache[key] = text
	h.mu.Unlock()
	return text, nil
}

// Subscribe 订阅 sub 子目录：该子目录下有文件增/删/改 → fn(relPath) 被调用。
// fn 在独立 goroutine 执行，不阻塞监控循环；重复通知由消费端幂等处理
// （重建不可变快照天然幂等）。要求 sub 已 Register。
func (h *ScriptHub) Subscribe(sub string, fn func(relPath string)) error {
	if fn == nil {
		return fmt.Errorf("hotswap: ScriptHub.Subscribe(%q) 回调为 nil", sub)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.dirs[sub]; !ok {
		return fmt.Errorf("hotswap: ScriptHub.Subscribe(%q) 子目录未注册", sub)
	}
	h.subs[sub] = append(h.subs[sub], fn)
	return nil
}

// Start 启动监控循环（幂等；随 Manager 生命周期启停，停机后不重启）。
// ★ 基线构建在 Start 内同步完成（Start 返回前指纹基线已就绪）：若把 buildBaseline
// 放进监控 goroutine，Start 返回后到 goroutine 实际调度前存在竞态窗口——
// 该窗口内写入的文件会被基线捕获，后续轮询永远视为"无变化"、永不通知。
// 语义保证：Start 返回后发生的任何文件增/删/改，均会在 ≤ interval 内触发订阅通知。
func (h *ScriptHub) Start() {
	h.startOnce.Do(func() {
		h.buildBaseline()
		h.wg.Add(1)
		go h.watchLoop()
	})
}

// Shutdown 停止监控循环并等待退出（幂等）。
func (h *ScriptHub) Shutdown() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})
	h.wg.Wait()
}

// watchLoop 监控主循环：周期指纹轮询（基线已由 Start 同步构建）。
func (h *ScriptHub) watchLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.pollOnce()
		}
	}
}

// buildBaseline 对全部已注册子目录扫描一次外挂目录树，仅填充指纹基线
// （避免启动即把既有文件当作"新增"触发一次无意义全量通知）。
func (h *ScriptHub) buildBaseline() {
	for _, sub := range h.subDirs() {
		h.mu.Lock()
		for rel, fp := range h.scan(sub) {
			h.fp[subKey(sub, rel)] = fp
		}
		h.mu.Unlock()
	}
}

// pollOnce 对全部已注册子目录做一轮指纹比对。
func (h *ScriptHub) pollOnce() {
	for _, sub := range h.subDirs() {
		h.pollSub(sub)
	}
}

// pollSub 单子目录一轮：扫描 → 差分出变化文件 → 逐个重读/更新缓存/通知。
// 变化处理（handleChange，含磁盘 I/O）在锁外进行，避免持锁做 I/O 阻塞并发读取；
// 重读入缓存后才通知订阅者，保证"回调内 GetScriptText 必拿到新内容"。
func (h *ScriptHub) pollSub(sub string) {
	h.mu.RLock()
	sd := h.dirs[sub]
	h.mu.RUnlock()

	current := h.scan(sub)

	h.mu.Lock()
	changed := h.diffFPs(sub, current)
	h.mu.Unlock()

	for _, relPath := range changed {
		h.handleChange(sub, sd, relPath)
	}
}

// diffFPs 与上次指纹比对，返回变化文件（增/删/改任一），并推进指纹基线。需持锁。
func (h *ScriptHub) diffFPs(sub string, current map[string]fileFP) []string {
	prefix := sub + "/"
	var changed []string
	for rel, fp := range current {
		key := subKey(sub, rel)
		if old, ok := h.fp[key]; !ok || old != fp {
			changed = append(changed, rel)
		}
		h.fp[key] = fp
	}
	// 删除检测：基线中有、本次扫描无（外挂文件被删 → 重读回退内嵌兜底）。
	// ★ 仅删除确实消失文件的指纹；仍存在的文件指纹保留（否则下轮误判为"新增"重复通知）。
	for key := range h.fp {
		rel, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		if _, ok := current[rel]; !ok {
			changed = append(changed, rel)
			delete(h.fp, key)
		}
	}
	sort.Strings(changed) // 确定性的处理顺序（也便于测试断言）
	return changed
}

// handleChange 处理单个文件变化：重读 → 更新缓存 → 通知订阅者。
// 读失败（外挂缺失且内嵌亦缺失）保留旧内容、仅告警，不通知、不中断。
func (h *ScriptHub) handleChange(sub string, sd *ScriptDir, relPath string) {
	key := subKey(sub, relPath)
	b, err := sd.GetScriptBytes(relPath)
	if err != nil {
		log.Warn("hotswap: ScriptHub 重读外挂文件失败，保留旧内容",
			"sub", sub, "file", relPath, "err", err.Error())
		return
	}

	h.mu.Lock()
	h.cache[key] = string(b)
	h.mu.Unlock()

	h.notify(sub, relPath)
}

// notify 通知 sub 子目录的全部订阅者。
// 先复制订阅者快照再逐一 go fn（独立 goroutine，互不阻塞、不阻塞监控循环）：
// 同一子目录多个订阅者并发执行、各自重建快照；重复通知由消费端幂等处理
// （重建不可变快照天然幂等）。
func (h *ScriptHub) notify(sub, relPath string) {
	h.mu.RLock()
	fns := append([]func(string){}, h.subs[sub]...)
	h.mu.RUnlock()
	for _, fn := range fns {
		go fn(relPath)
	}
}

// scan 递归扫描子目录外挂树（HOT_SCRIPTS_DIR/<sub>/），返回 相对路径 → 指纹。
// 根目录不存在 → 空集合（生产默认零开销：监控永不触发）；单个文件 stat 失败忽略。
func (h *ScriptHub) scan(sub string) map[string]fileFP {
	root := filepath.Join(hotScriptsDir, sub)
	fps := make(map[string]fileFP)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll // 根目录不存在：整体跳过（零开销）
			}
			return nil // 单路径 stat 失败（如权限）：忽略不中断遍历
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		fps[filepath.ToSlash(rel)] = fileFP{size: info.Size(), mtime: info.ModTime().UnixNano()}
		return nil
	})
	return fps
}

// subDirs 返回已注册子目录名（排序，保证轮询顺序确定）。
func (h *ScriptHub) subDirs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.dirs))
	for sub := range h.dirs {
		out = append(out, sub)
	}
	sort.Strings(out)
	return out
}

// subKey 统一缓存/指纹 key：子目录/相对路径（跨平台斜杠归一）。
func subKey(sub, relPath string) string {
	return sub + "/" + filepath.ToSlash(relPath)
}
