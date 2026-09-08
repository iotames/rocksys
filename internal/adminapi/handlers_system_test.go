package adminapi

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// selfStatLine 构造 /proc/self/stat 内容：comm 含空格与括号（最坏解析场景）。
// ')' 后字段从原第 3 字段 state 起：下标 0=state，11=utime（原 14 字段），12=stime（原 15 字段）。
func selfStatLine(utime, stime string) string {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "R"
	fields[11] = utime
	fields[12] = stime
	return "1234 (kworker/u8:1-业务) " + strings.Join(fields, " ") + "\n"
}

func TestParseSelfStatCPU(t *testing.T) {
	// utime=100 stime=50 → 150 jiffies；comm 含空格与括号不得干扰偏移
	procJ, ok := parseSelfStatCPU([]byte(selfStatLine("100", "50")))
	if !ok || procJ != 150 {
		t.Fatalf("parseSelfStatCPU = %v, %v; want 150, true", procJ, ok)
	}
	// 无 ')' 的残缺内容 → 不可得
	if _, ok := parseSelfStatCPU([]byte("1234 broken")); ok {
		t.Fatal("残缺内容应解析失败")
	}
}

func TestParseProcStatTotal(t *testing.T) {
	content := "cpu  10 0 20 1000 0 0 0 0 0 0\ncpu0 5 0 10 500 0 0 0 0 0 0\ncpu1 5 0 10 500 0 0 0 0 0 0\n"
	total, idle, ok := parseProcStatTotal([]byte(content))
	// 只取首行 cpu 汇总：total=10+0+20+1000=1030，idle=idle+iowait=1000+0=1000（不累加 cpu0/cpu1）
	if !ok || total != 1030 || idle != 1000 {
		t.Fatalf("parseProcStatTotal = %v, %v, %v; want 1030, 1000, true", total, idle, ok)
	}
	// 老内核仅 5 列（无 iowait）：idle 取得到、iowait 按 0
	if _, idle, _ := parseProcStatTotal([]byte("cpu  1 2 3 900\n")); idle != 900 {
		t.Fatalf("老内核 idle 解析失败: %v, want 900", idle)
	}
	// guest/guest_nice 已计入 user/nice，不得重复累加：total=100+0+20+1000=1120（guest 30 不再加）
	total, _, ok = parseProcStatTotal([]byte("cpu  100 0 20 1000 0 0 0 0 30 0\n"))
	if !ok || total != 1120 {
		t.Fatalf("guest 列重复累加: total=%v, ok=%v; want 1120, true", total, ok)
	}
	if _, _, ok := parseProcStatTotal([]byte("intr 0\n")); ok {
		t.Fatal("无 cpu 行应解析失败")
	}
}

func TestParseMemInfo(t *testing.T) {
	// 常规：MemAvailable 存在
	total, avail, ok := parseMemInfo([]byte("MemTotal: 16384 kB\nMemFree: 100 kB\nMemAvailable: 8192 kB\nCached: 50 kB\n"))
	if !ok || total != 16384*1024 || avail != 8192*1024 {
		t.Fatalf("parseMemInfo = %v, %v, %v", total, avail, ok)
	}
	// 老内核：无 MemAvailable → 回退 MemFree+Buffers+Cached
	total, avail, ok = parseMemInfo([]byte("MemTotal: 16384 kB\nMemFree: 100 kB\nBuffers: 200 kB\nCached: 300 kB\n"))
	if !ok || avail != 600*1024 {
		t.Fatalf("MemAvailable 缺失回退失败 = %v, %v, %v", total, avail, ok)
	}
	// 无 MemTotal → 不可得
	if _, _, ok := parseMemInfo([]byte("MemFree: 100 kB\n")); ok {
		t.Fatal("无 MemTotal 应解析失败")
	}
}

func TestSysSamplerSample(t *testing.T) {
	clk := time.Now() // 可拨动的假时钟：节流判断与采样时刻同源
	var totalJ, idleJ, procJ float64 = 1000, 800, 10
	mk := func() cpuSample { // 快照时刻即当前 clk（不自拨，dt 由测试手拨精确控制）
		return cpuSample{at: clk, procJ: procJ, totalJ: totalJ, idleJ: idleJ, ok: true, okTotal: true}
	}
	ss := &sysSampler{read: mk, now: func() time.Time { return clk }}

	// 首次采样：无前次快照可比 → 均 ok=false（前端按 null 处理，不显示误导性 0%）
	if _, _, okC, okP := ss.sample(); okC || okP {
		t.Fatal("首次采样应返回 ok=false")
	}
	// 间隔不足 3s：必须复用缓存（不推进快照、不产生有效值）
	if _, _, okC, okP := ss.sample(); okC || okP {
		t.Fatal("节流窗口内应复用缓存返回 ok=false")
	}
	// 拨到节流窗口之外并推进计数：3s 内 total 差 2000、idle 差 1600 → busy 400/2000 = 20%
	clk = clk.Add(3 * time.Second)
	totalJ += 2000
	idleJ += 1600
	procJ += 250
	cpuPct, procPct, okC, okP := ss.sample()
	if !okC || !okP {
		t.Fatalf("达到采样间隔且数据推进后应 ok=true: okCPU=%v okProc=%v", okC, okP)
	}
	// busy/total 口径：若误用 total 差值当分子（含 idle），此处会得到 ≈333% 而非 20%
	if cpuPct < 19.9 || cpuPct > 20.1 {
		t.Fatalf("全机 CPU%% busy/total 口径不符: %v, want ~20", cpuPct)
	}
	// 进程口径：dProc=250、dt=3s → 250/(3×100)×100 ≈ 83.3（防公式被改坏成数量级错误）
	if procPct < 80 || procPct > 87 {
		t.Fatalf("进程 CPU%% 计算不符: %v, want ~83.3", procPct)
	}
}

func TestSysSamplerProcIndependent(t *testing.T) {
	// /proc/stat 不可得（okTotal=false）时进程值仍应单独就绪（加固容器等场景）
	clk := time.Now()
	mk := func() cpuSample {
		clk = clk.Add(time.Second)
		return cpuSample{at: clk, procJ: 100, ok: true, okTotal: false}
	}
	ss := &sysSampler{read: mk, now: func() time.Time { return clk }}
	ss.sample()                    // 首次：真实读取（clk 推进 1s）
	clk = clk.Add(3 * time.Second) // 拨出节流窗口再采样
	_, procPct, okC, okP := ss.sample()
	if okC {
		t.Fatal("okTotal=false 时全机 CPU 不应就绪")
	}
	if !okP || procPct != 0 {
		t.Fatalf("进程 CPU 应独立就绪且为 0（计数未推进）: okProc=%v procPct=%v", okP, procPct)
	}
}

func TestSysSamplerThrottleSkipsRead(t *testing.T) {
	calls := 0
	clk := time.Now()
	ss := &sysSampler{
		read: func() cpuSample {
			calls++
			clk = clk.Add(time.Second)
			return cpuSample{at: clk, procJ: 0, totalJ: 100, ok: true, okTotal: true}
		},
		now: func() time.Time { return clk },
	}
	ss.sample() // 首次：真实读取
	ss.sample() // 节流窗口内：不得再读
	if calls != 1 {
		t.Fatalf("节流窗口内 read 被调用了 %d 次, want 1", calls)
	}
}

func TestRound1(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0, 0}, {3.21, 3.2}, {3.25, 3.3}, {99.96, 100}, {12.34, 12.3},
	}
	for _, c := range cases {
		if got := round1(c.in); got != c.want {
			t.Errorf("round1(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestHandleSystem 验证契约形态：200 + 关键字段在位；进程级字段全平台可用。
// CPU 两字段为 null（包级采样器无历史快照可比）或数值（-count=2 等场景已有历史快照，
// proc 值可超 100）均属合法契约，只验类型不锁具体值，避免依赖包级单例的采样时序。
func TestHandleSystem(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)
	ctx := newCtx(http.MethodGet, PathSystem, "")
	s.handleSystem(ctx)
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d, want 200", rec.Code)
	}
	out := decode(t, ctx)
	if _, ok := out["uptime_seconds"].(float64); !ok {
		t.Errorf("uptime_seconds 缺失或非数值: %v", out["uptime_seconds"])
	}
	if s, _ := out["started_at"].(string); s == "" {
		t.Error("started_at 缺失")
	}
	// CPU 两字段：null（无历史快照可比）或数值均合法（-count=2 下采样器可能有历史快照）
	for _, k := range []string{"cpu_percent", "proc_cpu_percent"} {
		switch v := out[k].(type) {
		case nil:
		case float64:
			if v < 0 {
				t.Errorf("%s = %v, want ≥0", k, v)
			}
		default:
			t.Errorf("%s 应为 null 或数值: %v", k, out[k])
		}
	}
	if out["num_cpu"] != float64(runtime.NumCPU()) {
		t.Errorf("num_cpu = %v, want %d", out["num_cpu"], runtime.NumCPU())
	}
	if out["os"] != runtime.GOOS || out["arch"] != runtime.GOARCH {
		t.Errorf("os/arch = %v/%v, want %s/%s", out["os"], out["arch"], runtime.GOOS, runtime.GOARCH)
	}
	for _, k := range []string{"proc_mem_bytes", "heap_bytes", "goroutines"} {
		if v, ok := out[k].(float64); !ok || v <= 0 {
			t.Errorf("%s = %v, want >0", k, out[k])
		}
	}
	// mem_total / mem_used 平台相关（Linux 数值、其他平台 null），只验证键在位
	for _, k := range []string{"mem_total", "mem_used"} {
		if _, ok := out[k]; !ok {
			t.Errorf("响应缺少键 %s", k)
		}
	}
}
