// FileStore 访问日志文件存储后端（OBS_STORE=file，默认）：
// 同步批量追加写 logs/access-YYYY-MM-DD.jsonl（每行一个平铺维度 JSON），
// 按天切分、超期清理；Query 跨天读文件逐行解析并按条件过滤。
// 异步排队由 AsyncStore 承担，本实现只做同步 IO。
package obs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileStore 文件存储后端。
type FileStore struct {
	dir           string
	retentionDays int

	mu      sync.Mutex // 保护 f/curDate/dir/retentionDays
	f       *os.File
	curDate string
}

// NewFileStore 创建文件存储后端（目录懒创建，首次写入时 MkdirAll）。
func NewFileStore(dir string, retentionDays int) *FileStore {
	return &FileStore{dir: dir, retentionDays: retentionDays}
}

// Name 后端名。
func (s *FileStore) Name() string { return "file" }

// Write 同步追加一批记录到当天文件；跨天切分新文件并执行留存清理。
func (s *FileStore) Write(batch []*AccessRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := s.ensureDayFile(); err != nil {
		return err
	}
	var buf strings.Builder
	for _, r := range batch {
		line, err := json.Marshal(r.ToFlatMap())
		if err != nil {
			continue // 单条序列化失败单独丢弃
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return nil
	}
	if _, err := s.f.WriteString(buf.String()); err != nil {
		return err
	}
	return nil
}

// Query 跨天读文件，逐行解析平铺维度并按条件过滤，返回 time 倒序列表（最新在前）。
func (s *FileStore) Query(q Query) ([]map[string]any, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	var out []map[string]any
	day := time.Date(q.To.Year(), q.To.Month(), q.To.Day(), 0, 0, 0, 0, time.Local)
	endDay := time.Date(q.From.Year(), q.From.Month(), q.From.Day(), 0, 0, 0, 0, time.Local)
	for !day.Before(endDay) {
		path := filepath.Join(s.dir, "access-"+day.Format("2006-01-02")+".jsonl")
		if f, err := os.Open(path); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			var lines []map[string]any
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					continue // 坏行容错跳过
				}
				if !matchQuery(m, q) {
					continue
				}
				lines = append(lines, m)
			}
			_ = f.Close()
			// 文件内行倒序（新写入在前），与 DB 的 id DESC 语义一致
			for i := len(lines) - 1; i >= 0; i-- {
				out = append(out, lines[i])
				if len(out) >= limit {
					break
				}
			}
		}
		if len(out) >= limit {
			break
		}
		day = day.AddDate(0, 0, -1)
	}
	return out, nil
}

// Flush 无缓冲（异步队列在 AsyncStore），直接返回。
func (s *FileStore) Flush(ctx context.Context) error { return nil }

// Close 关闭当前文件句柄。
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeFile()
}

// ensureDayFile 确保当前句柄对应当天文件；跨天/空句柄时切分并清理旧日志（调用方持锁）。
func (s *FileStore) ensureDayFile() error {
	today := time.Now().Format("2006-01-02")
	if s.f != nil && s.curDate == today {
		return nil
	}
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	if s.dir == "" {
		s.dir = defaultLogDir
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("obs: 创建日志目录 %s: %w", s.dir, err)
	}
	path := filepath.Join(s.dir, "access-"+today+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("obs: 打开日志文件 %s: %w", path, err)
	}
	s.f = f
	s.curDate = today
	s.cleanupOld()
	return nil
}

// closeFile 关闭当前文件句柄（调用方持锁）。
func (s *FileStore) closeFile() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	s.curDate = ""
	return err
}

// cleanupOld 清理超过留存天数的历史日志文件（调用方持锁）。
func (s *FileStore) cleanupOld() {
	if s.retentionDays <= 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "access-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, "access-"), ".jsonl")
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// SizeBytes 统计日志目录下所有 access-*.jsonl 文件的总字节数；目录不存在返回 0。
func (s *FileStore) SizeBytes() (int64, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "access-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total, nil
}

// matchQuery 按查询条件过滤一条平铺记录（time/path/path_like/trace_id）。
func matchQuery(m map[string]any, q Query) bool {
	t, ok := parseRowTime(m)
	if !ok {
		return false
	}
	if t.Before(q.From) || t.After(q.To) {
		return false
	}
	if q.Path != "" {
		if p, _ := m[DimPath].(string); p != q.Path {
			return false
		}
	}
	if q.PathLike != "" {
		if p, _ := m[DimPath].(string); !strings.Contains(p, q.PathLike) {
			return false
		}
	}
	if q.TraceID != "" {
		if tid, _ := m[DimTraceID].(string); !strings.Contains(tid, q.TraceID) {
			return false
		}
	}
	return true
}

// parseRowTime 解析平铺记录中的 time 维度（RFC3339），失败返回 false。
func parseRowTime(m map[string]any) (time.Time, bool) {
	raw, ok := m[DimTime]
	if !ok {
		return time.Time{}, false
	}
	switch v := raw.(type) {
	case string:
		t, err := time.Parse(time.RFC3339, v)
		return t, err == nil
	case time.Time:
		return v, true
	}
	return time.Time{}, false
}
