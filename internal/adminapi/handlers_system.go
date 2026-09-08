// GET /admin/system：进程运行时长 + 机器资源概况（概览页资源监控卡与运行指标瓦片用）。
//
// 实现约束（轻量优先，不引第三方依赖）：
//   - 无常驻采集协程，CPU% 走惰性采样：距上次真实采样不足 cpuSampleInterval 时
//     直接复用缓存结果、不读 /proc 也不推进快照（无人访问时零开销，高频访问被
//     节流为至多每间隔一次读取，且保证快照间隔足够算出差值）；
//   - 系统级 CPU% 按 busy/total 口径（与 top 一致）：全机时间片总和扣除 idle+iowait 的占比，
//     天然按核心归一化到 0-100；进程 CPU% 与 top 同口径，多线程可超 100；
//   - 系统级 CPU / 内存依赖 Linux /proc，非 Linux 平台对应字段返回 null，
//     前端降级展示（生产部署目标为 Linux，不受影响）；
//   - 进程级数据（运行时长 / 进程内存 / Goroutines）全平台可用（标准库 runtime）。
package adminapi

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iotames/easyserver/httpsvr"
)

// procStart 进程启动时刻（包初始化即进程装配初期，误差毫秒级，供运行时长计算）。
var procStart = time.Now()

// /proc 路径提为包级变量：测试可注入伪造内容（解析逻辑是本文件最该单测的部分）。
var (
	procSelfStatPath = "/proc/self/stat"
	procStatPath     = "/proc/stat"
	procMemInfoPath  = "/proc/meminfo"
)

const (
	// cpuSampleInterval CPU% 最小重采样间隔：低于该间隔复用缓存，避免高频请求反复读 /proc。
	cpuSampleInterval = 3 * time.Second
	// clockTicks Linux /proc 统计的时间单位（USER_HZ，固定 100）。
	clockTicks = 100
)

// cpuSample 一次 CPU 累计时间快照（用于差值计算占用率）。
type cpuSample struct {
	at      time.Time // 采样时刻
	procJ   float64   // 进程累计 CPU 时间（jiffies）
	totalJ  float64   // 全机所有核心各状态时间片之和（jiffies）
	idleJ   float64   // 全机 idle+iowait 时间片（jiffies，busy = total - idle）
	ok      bool      // 进程级数据是否可得（/proc/self/stat）
	okTotal bool      // 全机数据是否可得（/proc/stat）
}

// sysSampler 系统资源采样器（真实节流 + 快照缓存，进程内共享一份）。
// read / now 可注入替换（测试伪造 /proc 内容与时钟）；零值不可用，须用 newSysSampler 构造。
type sysSampler struct {
	mu        sync.Mutex
	read      func() cpuSample
	now       func() time.Time // 节流判断所用时钟（与 read 产出的 at 同源方可正确比较）
	last      cpuSample        // 上次真实采样快照（仅在真实采样时推进）
	sampled   bool             // 是否已真实读过至少一次（读失败也置位，保证节流在任何平台都生效）
	cpuPct    float64          // 上次算得的全机 CPU 占用（0-100，busy/total 口径）
	procPct   float64          // 上次算得的进程 CPU 占用（0-100+，多线程可超 100）
	readyCPU  bool             // cpuPct 是否已算出有效值（首次差值成立前为 false，前端按 null 处理）
	readyProc bool             // procPct 是否已算出有效值（与 readyCPU 独立：/proc/stat 不可得时进程值仍可单独得出）
}

// newSysSampler 构造使用真实 /proc 读取的采样器。
func newSysSampler() *sysSampler {
	return &sysSampler{read: readProcCPUSample, now: time.Now}
}

// defaultSysSampler adminapi 进程内共享采样器（管理接口为单实例，无需挂在 AdminServer 上）。
var defaultSysSampler = newSysSampler()

// readProcCPUSample 读取当前 CPU 累计时间快照。
// 进程级读 /proc/self/stat（utime+stime）；全机读 /proc/stat 首行 cpu 汇总（总和与 idle+iowait）。
// 非 Linux 或读取失败时 ok=false（进程级）/ okTotal=false（全机级）。
func readProcCPUSample() (s cpuSample) {
	s.at = time.Now()
	if b, err := os.ReadFile(procSelfStatPath); err == nil {
		s.procJ, s.ok = parseSelfStatCPU(b)
	}
	if b, err := os.ReadFile(procStatPath); err == nil {
		s.totalJ, s.idleJ, s.okTotal = parseProcStatTotal(b)
	}
	return s
}

// parseSelfStatCPU 从 /proc/self/stat 内容解析进程累计 CPU 时间（utime+stime，jiffies）。
// 第 2 字段 comm 可含空格与括号，从最后一个 ')' 之后切分：fields[0] 对应原第 3 字段
// state，utime/stime 为原第 14/15 字段 → 切分后下标 11/12。
func parseSelfStatCPU(b []byte) (float64, bool) {
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 13 {
		return 0, false
	}
	ut, _ := strconv.ParseFloat(fields[11], 64)
	st, _ := strconv.ParseFloat(fields[12], 64)
	return ut + st, true
}

// parseProcStatTotal 从 /proc/stat 内容解析首行 cpu 汇总（jiffies）。
// 返回各状态列之和 total 与其中 idle+iowait 之和 idle（busy = total - idle，与 top 同口径）。
// 列序：user nice system idle iowait irq softirq steal guest guest_nice；guest/guest_nice
// 已计入 user/nice（Linux ≥2.6.24），不重复累加（与 gopsutil/htop 同口径）；老内核列数少时
// 缺失的 idle/iowait 按 0 计（此时 CPU% 偏高属可接受的降级，不影响进程级数据）。
func parseProcStatTotal(b []byte) (total, idle float64, ok bool) {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			n := len(fields)
			if n > 9 { // 只累加到 steal（下标 8），排除 guest/guest_nice
				n = 9
			}
			for _, f := range fields[1:n] {
				v, _ := strconv.ParseFloat(f, 64)
				total += v
			}
			if len(fields) > 4 {
				idle, _ = strconv.ParseFloat(fields[4], 64)
			}
			if len(fields) > 5 {
				iowait, _ := strconv.ParseFloat(fields[5], 64)
				idle += iowait
			}
			return total, idle, true
		}
	}
	return 0, 0, false
}

// sample 返回当前 CPU 占用（全机 / 进程）。
// 全机值为 busy/total 口径（0-100，与 top 一致）；进程值与 top 同口径（多线程可超 100）。
// okCPU/okProc 表示对应值是否已算出：距上次真实采样不足 cpuSampleInterval 时直接复用缓存
// （不读 /proc、不推进快照——否则快速连续请求会使快照间隔永远达不到阈值，差值永不成立）；
// 首次调用尚无前次快照可比，对应 ok 为 false，由调用方置 null。
func (ss *sysSampler) sample() (cpuPct, procPct float64, okCPU, okProc bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.sampled && ss.now().Sub(ss.last.at) < cpuSampleInterval {
		return ss.cpuPct, ss.procPct, ss.readyCPU, ss.readyProc
	}
	ss.sampled = true
	now := ss.read()
	// 进程 CPU%：累计 jiffies 差值 / 经历时长（经历时长须为正才可除）
	if now.ok && ss.last.ok && now.at.After(ss.last.at) {
		dt := now.at.Sub(ss.last.at).Seconds()
		dProc := now.procJ - ss.last.procJ
		if dProc < 0 {
			dProc = 0
		}
		ss.procPct = dProc / (dt * clockTicks) * 100
		ss.readyProc = true
	}
	// 全机 CPU%：busy（total-idle）占 total 的差值比。total 含 idle——每个时钟节拍必然
	// 记入某一状态列，直接拿 total 差值当分子会恒 ≈100%，故必须扣除 idle+iowait。
	if now.okTotal && ss.last.okTotal && now.at.After(ss.last.at) {
		dTotal := now.totalJ - ss.last.totalJ
		if dTotal > 0 {
			p := (dTotal - (now.idleJ - ss.last.idleJ)) / dTotal * 100
			if p < 0 {
				p = 0
			} else if p > 100 {
				p = 100
			}
			ss.cpuPct = p
			ss.readyCPU = true
		}
	}
	ss.last = now
	return ss.cpuPct, ss.procPct, ss.readyCPU, ss.readyProc
}

// parseMemInfo 从 /proc/meminfo 内容解析内存总量与可用量（字节）。
// MemAvailable 缺失（内核 <3.14）时回退 MemFree + Buffers + Cached。
func parseMemInfo(b []byte) (total, available uint64, ok bool) {
	get := func(key string) (uint64, bool) {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, key+":") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					v, _ := strconv.ParseUint(fields[1], 10, 64)
					return v * 1024, true // kB → B
				}
				return 0, false
			}
		}
		return 0, false
	}
	total, ok = get("MemTotal")
	if !ok {
		return 0, 0, false
	}
	if available, ok = get("MemAvailable"); !ok {
		free, okFree := get("MemFree")
		buffers, okBuf := get("Buffers")
		cached, okCached := get("Cached")
		if okFree && okBuf && okCached {
			available = free + buffers + cached
		}
	}
	return total, available, true
}

// readMemInfo 读取 Linux /proc/meminfo 的内存总量与可用量（字节）。非 Linux 返回 ok=false。
func readMemInfo() (total, available uint64, ok bool) {
	b, err := os.ReadFile(procMemInfoPath)
	if err != nil {
		return 0, 0, false
	}
	return parseMemInfo(b)
}

// handleSystem 返回运行时长与资源概况；字段不可得时为 null，前端降级展示。
func (s *AdminServer) handleSystem(ctx httpsvr.Context) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	sysTotal, sysAvail, memOK := readMemInfo()
	cpuPct, procPct, okCPU, okProc := defaultSysSampler.sample()

	resp := map[string]any{
		"uptime_seconds":   int64(time.Since(procStart).Seconds()),
		"started_at":       procStart.Format(time.RFC3339),
		"proc_mem_bytes":   ms.Sys,       // 进程从 OS 获取的内存总量（含堆/栈/运行时元数据）
		"heap_bytes":       ms.HeapAlloc, // 在用堆内存
		"goroutines":       runtime.NumGoroutine(),
		"num_cpu":          runtime.NumCPU(),
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"cpu_percent":      nil,
		"proc_cpu_percent": nil,
		"mem_total":        nil,
		"mem_used":         nil,
	}
	// 全机 / 进程 CPU 就绪相互独立：/proc/stat 不可得（如加固容器）时进程值仍可单独得出
	if okCPU {
		resp["cpu_percent"] = round1(cpuPct)
	}
	if okProc {
		resp["proc_cpu_percent"] = round1(procPct)
	}
	if memOK {
		resp["mem_total"] = sysTotal
		resp["mem_used"] = sysTotal - sysAvail
	}
	_ = writeJSON(ctx.Writer, resp, 200)
}

// round1 保留一位小数（CPU 占用展示精度足够）。
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
