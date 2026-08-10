package hotswap

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// TestGetScriptBytes_TriState 收敛核心入口三态：
// 外挂存在且非空 → 外挂；外挂 0 字节/缺失 → 回退内嵌；内嵌缺失 → error。
func TestGetScriptBytes_TriState(t *testing.T) {
	orig := HotScriptsDir()
	t.Cleanup(func() { SetHotScriptsDir(orig) })
	ext := t.TempDir()
	SetHotScriptsDir(ext)

	embedFS := fstest.MapFS{
		"x.txt": {Data: []byte("embedded")},
	}
	mk := func(sub, name, content string) {
		p := filepath.Join(ext, sub, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		sub     string // 业务子目录
		file    string // 文件相对路径（业务子目录内）
		setup   func()
		want    string
		wantErr bool
	}{
		{"外挂非空优先", "rules", "x.txt", func() { mk("rules", "x.txt", "external") }, "external", false},
		{"外挂0字节回退内嵌", "rules", "x.txt", func() { mk("rules", "x.txt", "") }, "embedded", false},
		{"外挂缺失回退内嵌", "rules", "x.txt", func() {}, "embedded", false},
		{"内嵌缺失报错", "rules", "y.txt", func() {}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.setup()
			sd := NewScriptDir(embedFS, c.sub)
			b, err := sd.GetScriptBytes(c.file)
			if c.wantErr {
				if err == nil {
					t.Fatal("应返回 error")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetScriptBytes: %v", err)
			}
			if string(b) != c.want {
				t.Errorf("GetScriptBytes = %q, want %q", string(b), c.want)
			}
		})
	}
}
