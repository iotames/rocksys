// Copyright © 进程日志管理端点。
//
// 提供 5 个 /admin/log/* 端点：info（状态）/level（级别热切）/output（文件通道热切）/
// tail（HTTP 轮询）/stream（SSE 实时推送），全部走现有 requireAuth 鉴权。
package adminapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iotames/easyserver/httpsvr"
	"github.com/iotames/easyserver/log"
)

// slogLevel 将配置字符串映射为 slog.Level（debug/info/warn/error；未知默认 info）。
// 必须在 adminapi 包内自行定义——cmd/rocksys/main 包的 slogLevel 不可见。
func slogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// validLevel 校验级别字符串合法性（debug/info/warn/warning/error）。
func validLevel(s string) bool {
	switch strings.ToLower(s) {
	case "debug", "info", "warn", "warning", "error":
		return true
	}
	return false
}

// parseN 解析 n 参数；非法回退 def；结果夹取到 [1,1000]
// （避免 ?n=0/-1 绕过上限拉到全量 8MB）。
func parseN(v string, def int) int {
	n := def
	if i, err := strconv.Atoi(v); err == nil {
		n = i
	}
	if n < 1 {
		n = 1
	}
	if n > 1000 {
		n = 1000
	}
	return n
}

// parseSince 解析 since 参数；缺省/非法/任意负数返回 -1 表示「尾部首拉」
// （首次请求取窗口尾部最后 n 行）。since==-1 直接透传给 log.Tail（Tail 内部处理尾部首拉）。
// ⚠️ since=0 是合法增量游标（从窗口起点读 n 行），将返回窗口最旧 n 行而非尾部 n 行。
func parseSince(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// handleLogInfo 返回当前日志状态（级别/模板/文件/ring 状态）。
func (s *AdminServer) handleLogInfo(ctx httpsvr.Context) {
	// 用 writeJSON 传值 log.GetInfo()——ctx.Json 只接受 map[string]any，不能直接传 LogInfo。
	_ = writeJSON(ctx.Writer, log.GetInfo(), http.StatusOK)
}

// handleLogLevel 热切级别并持久化：{"level":"debug"} → log.SetLevel + 钩子 conf.Set 写盘。
func (s *AdminServer) handleLogLevel(ctx httpsvr.Context) {
	var body struct {
		Level string `json:"level"`
	}
	if err := ctx.GetPostJson(&body); err != nil || body.Level == "" {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body"}, http.StatusBadRequest)
		return
	}
	// 校验级别合法（debug/info/warn/error）。
	if !validLevel(body.Level) {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid level, allow debug/info/warn/warning/error"}, http.StatusBadRequest)
		return
	}
	// 生效：触发 SetOnLevelChange 钩子 → conf.Set("ROCKSYS_LOG_LEVEL") → 写盘（main.go 装配注入）。
	// ⚠️ M7 二选一（本实现取「持久化失败静默」）：持久化钩子位于 main 装配层（SetOnLevelChange
	//   为单槽注册，adminapi 无法叠加读取其错误），故持久化失败静默为既定行为，返回 200。
	log.SetLevel(slogLevel(body.Level))
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleLogOutput 热切文件通道并持久化：{"file":true|false}。
// ★ 开启时序（M6）：先开新句柄成功 → 再 SetMaxSize → 再持久化 ROCKSYS_LOG_TO_FILE=true。
//
//	SetLogWriterByFile 内部先 newFileWriter（失败返回 err、旧句柄未动），成功后才替换，
//	保证「先开新句柄成功再持久化」——打开失败时不丢旧存档、不写盘。
func (s *AdminServer) handleLogOutput(ctx httpsvr.Context) {
	var body struct {
		File bool `json:"file"`
	}
	if err := ctx.GetPostJson(&body); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body"}, http.StatusBadRequest)
		return
	}
	if body.File {
		if _, err := log.SetLogWriterByFile(s.confMgr.Current().LogFile); err != nil {
			// 打开失败（路径不可写/父目录创建失败）→ 旧句柄仍在 → 500 且不写 ROCKSYS_LOG_TO_FILE=true。
			_ = ctx.Json(map[string]any{"ok": false, "error": "open log file: " + err.Error()}, http.StatusInternalServerError)
			return
		}
		log.SetMaxSize(s.confMgr.Current().LogMaxSize)
		if err := s.confMgr.Set("ROCKSYS_LOG_TO_FILE", "true"); err != nil {
			// 文件通道已生效但持久化失败 → 500 告警（热更已生效，落盘失败需知晓）。
			_ = ctx.Json(map[string]any{"ok": false, "error": "persist ROCKSYS_LOG_TO_FILE: " + err.Error()}, http.StatusInternalServerError)
			return
		}
	} else {
		log.SetFileWriter(false)
		if err := s.confMgr.Set("ROCKSYS_LOG_TO_FILE", "false"); err != nil {
			_ = ctx.Json(map[string]any{"ok": false, "error": "persist ROCKSYS_LOG_TO_FILE: " + err.Error()}, http.StatusInternalServerError)
			return
		}
	}
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleLogTail 增量读取 ring buffer（HTTP 轮询，AI 主路径）。
// 首次 since 缺省 → 尾部首拉（窗口尾部最后 n 行）；增量续读从 next_offset 起；
// since 已被覆盖 → reset=true，客户端应丢弃本次 lines、以缺省 since 重新尾部首拉。
func (s *AdminServer) handleLogTail(ctx httpsvr.Context) {
	n := parseN(ctx.Request.URL.Query().Get("n"), 100)
	since := parseSince(ctx.Request.URL.Query().Get("since"))
	// since==-1 透传给 Tail 做「尾部首拉」（取窗口尾部最后 n 行），
	// 保证首次 ?n=100 拉到的是尾部 100 行而非窗口最旧 100 行。
	res := log.Tail(int64(n), since)
	// ★ L6：Lines 为空时序列化为空数组而非 null（与设计示例 "lines":[...] 一致）。
	lines := res.Lines
	if lines == nil {
		lines = []string{}
	}
	_ = writeJSON(ctx.Writer, map[string]any{
		"lines":       lines,
		"next_offset": res.NextOffset,
		"eof":         res.EOF,
		"reset":       res.Reset,
	}, http.StatusOK)
}

// handleLogStream SSE 实时推送：订阅 ring buffer，新日志即推；断线重连从最新开始（不续读历史）。
func (s *AdminServer) handleLogStream(ctx httpsvr.Context) {
	w := ctx.Writer
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// 从最新开始：用 GetInfo().RingTotal 作为起始游标——Tail(since>=total) 返回 EOF，
	// 后续轮询只取新日志；切勿用 math.MaxInt64（恒 EOF 导致一行都不推）。
	// ★ 先取 since 快照再 Flush：SSE 测试以首次 Flush 为「连接已建立」信号，
	//   notify 关闭时 since 已确定，测试在 notify 后写日志不会漏推（避免快照竞态）。
	since := log.GetInfo().RingTotal
	fl.Flush()
	for {
		res := log.Tail(100, since)
		// ★ 无条件推进游标：Tail 在 since 已被覆盖时返回 Reset=true 且 Lines 为空，
		//   若只在 len(Lines)>0 时推进，8MB 溢出后将永远 Reset→空，SSE 永久停推。
		since = res.NextOffset
		for _, line := range res.Lines {
			// ★ M3/L4：msg 含裸 \n/\r 时替换为空格，避免破坏 event-stream 帧格式。
			line = strings.ReplaceAll(line, "\n", " ")
			line = strings.ReplaceAll(line, "\r", "")
			fmt.Fprintf(w, "data: %s\n\n", line)
		}
		if len(res.Lines) > 0 {
			fl.Flush()
		}
		select {
		case <-ctx.Request.Context().Done():
			return // 客户端断开，goroutine 退出，无泄漏
		case <-time.After(500 * time.Millisecond):
		}
		// 心跳：无条件每 500ms 发送注释行 `: ping`（防止代理空闲断开；简单可靠，不判断是否有日志）。
		fmt.Fprintf(w, ": ping\n\n")
		fl.Flush()
	}
}
