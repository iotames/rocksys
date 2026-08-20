package hotswap

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// hubTestEnv 构造隔离的外挂目录与嵌入 FS，返回 hub 与辅助写文件函数。
// t.Cleanup 恢复全局 hotScriptsDir，避免污染其他测试。
func hubTestEnv(t *testing.T, embedFiles map[string]string) (*ScriptHub, func(sub, rel, content string)) {
	t.Helper()
	orig := HotScriptsDir()
	t.Cleanup(func() { SetHotScriptsDir(orig) })
	ext := t.TempDir()
	SetHotScriptsDir(ext)

	embedFS := fstest.MapFS{}
	for name, content := range embedFiles {
		embedFS[name] = &fstest.MapFile{Data: []byte(content)}
	}

	mk := func(sub, rel, content string) {
		t.Helper()
		p := filepath.Join(ext, sub, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	hub := NewScriptHub(10 * time.Millisecond)
	return hub, mk
}

// registerSub 便捷注册：嵌入 FS 子目录下文件（sub 下相对路径）。
func registerSub(hub *ScriptHub, sub string, embedFiles map[string]string) *ScriptDir {
	embedFS := fstest.MapFS{}
	for name, content := range embedFiles {
		embedFS[name] = &fstest.MapFile{Data: []byte(content)}
	}
	sd := NewScriptDir(embedFS, sub)
	if err := hub.Register(sub, sd); err != nil {
		panic(err)
	}
	return sd
}

// TestScriptHub_GetScriptText_Cache 缓存命中：外挂变更后、轮询前 GetScriptText 仍返回旧内容（缓存未失效）。
func TestScriptHub_GetScriptText_Cache(t *testing.T) {
	hub, mk := hubTestEnv(t, nil)
	registerSub(hub, "rules", map[string]string{"crawler_ua.txt": "embedded"})
	mk("rules", "crawler_ua.txt", "v1")

	got, err := hub.GetScriptText("rules", "crawler_ua.txt")
	if err != nil {
		t.Fatalf("GetScriptText: %v", err)
	}
	if got != "v1" {
		t.Fatalf("首读 = %q, want v1（外挂优先）", got)
	}

	// 修改外挂文件，未轮询前：命中缓存，仍为 v1
	mk("rules", "crawler_ua.txt", "v2")
	got, err = hub.GetScriptText("rules", "crawler_ua.txt")
	if err != nil {
		t.Fatalf("GetScriptText: %v", err)
	}
	if got != "v1" {
		t.Fatalf("轮询前 = %q, want v1（缓存未失效）", got)
	}

	// 手动一轮轮询：缓存更新为 v2
	hub.pollOnce()
	got, err = hub.GetScriptText("rules", "crawler_ua.txt")
	if err != nil {
		t.Fatalf("GetScriptText: %v", err)
	}
	if got != "v2" {
		t.Fatalf("轮询后 = %q, want v2", got)
	}
}

// TestScriptHub_GetScriptText_未命中走底层 未注册子目录报错；未命中写缓存后可命中。
func TestScriptHub_GetScriptText_未注册报错与缓存写入(t *testing.T) {
	hub, _ := hubTestEnv(t, nil)

	if _, err := hub.GetScriptText("nope", "x.txt"); err == nil {
		t.Fatal("未注册子目录应报错")
	}

	registerSub(hub, "rules", map[string]string{"x.txt": "embedded"})
	// 无外挂 → 回退内嵌
	got, err := hub.GetScriptText("rules", "x.txt")
	if err != nil {
		t.Fatalf("GetScriptText: %v", err)
	}
	if got != "embedded" {
		t.Fatalf("回退内嵌 = %q, want embedded", got)
	}
}

// TestScriptHub_Subscribe_增改删均触发 订阅回调：新增、修改、删除外挂文件各触发一次，且回调时内容已更新。
func TestScriptHub_Subscribe_增改删均触发(t *testing.T) {
	hub, mk := hubTestEnv(t, nil)
	registerSub(hub, "rules", map[string]string{"crawler_ua.txt": "embedded"})

	events := make(chan string, 10)
	if err := hub.Subscribe("rules", func(relPath string) {
		events <- relPath
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 新增文件
	mk("rules", "risk_paths.txt", "add")
	hub.pollOnce()
	select {
	case rel := <-events:
		if rel != "risk_paths.txt" {
			t.Fatalf("新增通知 rel = %q, want risk_paths.txt", rel)
		}
	case <-time.After(time.Second):
		t.Fatal("新增文件未触发订阅通知")
	}

	// 修改文件
	mk("rules", "crawler_ua.txt", "v2")
	hub.pollOnce()
	select {
	case rel := <-events:
		if rel != "crawler_ua.txt" {
			t.Fatalf("修改通知 rel = %q, want crawler_ua.txt", rel)
		}
		got, err := hub.GetScriptText("rules", "crawler_ua.txt")
		if err != nil {
			t.Fatalf("GetScriptText: %v", err)
		}
		if got != "v2" {
			t.Fatalf("回调时内容 = %q, want v2（推送前已更新缓存）", got)
		}
	case <-time.After(time.Second):
		t.Fatal("修改文件未触发订阅通知")
	}

	// 删除文件 → 回退内嵌
	if err := os.Remove(filepath.Join(HotScriptsDir(), "rules", "crawler_ua.txt")); err != nil {
		t.Fatal(err)
	}
	hub.pollOnce()
	select {
	case rel := <-events:
		if rel != "crawler_ua.txt" {
			t.Fatalf("删除通知 rel = %q, want crawler_ua.txt", rel)
		}
		got, err := hub.GetScriptText("rules", "crawler_ua.txt")
		if err != nil {
			t.Fatalf("GetScriptText: %v", err)
		}
		if got != "embedded" {
			t.Fatalf("删除后回退内嵌 = %q, want embedded", got)
		}
	case <-time.After(time.Second):
		t.Fatal("删除文件未触发订阅通知")
	}
}

// TestScriptHub_监控循环StartShutdown Start 启动循环后写文件，interval 内自动触发订阅；Shutdown 幂等。
func TestScriptHub_监控循环StartShutdown(t *testing.T) {
	hub, mk := hubTestEnv(t, nil)
	registerSub(hub, "rules", map[string]string{})

	events := make(chan string, 4)
	if err := hub.Subscribe("rules", func(relPath string) { events <- relPath }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	hub.Start()
	mk("rules", "x.txt", "v1")
	select {
	case rel := <-events:
		if rel != "x.txt" {
			t.Fatalf("通知 rel = %q, want x.txt", rel)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start 后写文件 3s 内未触发订阅（监控循环未生效）")
	}

	// 幂等：重复 Start 不产生重复 goroutine（再次写文件应恰好 1 次通知）
	hub.Start()
	mk("rules", "y.txt", "v2")
	select {
	case rel := <-events:
		if rel != "y.txt" {
			t.Fatalf("通知 rel = %q, want y.txt", rel)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("重复 Start 后监控失效")
	}
	select {
	case rel := <-events:
		t.Fatalf("重复 Start 产生了重复通知: %q", rel)
	case <-time.After(50 * time.Millisecond):
	}

	hub.Shutdown()
	hub.Shutdown() // 幂等
}

// TestScriptHub_目录不存在零开销 未创建外挂目录：pollOnce 不触发、不报错（生产默认零开销）。
func TestScriptHub_目录不存在零开销(t *testing.T) {
	hub, _ := hubTestEnv(t, nil)
	registerSub(hub, "rules", map[string]string{"x.txt": "embedded"})

	events := make(chan string, 4)
	if err := hub.Subscribe("rules", func(relPath string) { events <- relPath }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	hub.pollOnce() // 外挂目录不存在：指纹集合为空，永不触发
	select {
	case rel := <-events:
		t.Fatalf("目录不存在不应触发通知: %q", rel)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestScriptHub_读失败保留旧内容 外挂文件删除且内嵌亦缺失：重读失败保留旧缓存、不通知。
func TestScriptHub_读失败保留旧内容(t *testing.T) {
	hub, mk := hubTestEnv(t, nil)
	registerSub(hub, "rules", map[string]string{}) // 内嵌空

	mk("rules", "x.txt", "v1")
	hub.pollOnce() // 建立基线
	if _, err := hub.GetScriptText("rules", "x.txt"); err != nil {
		t.Fatalf("GetScriptText: %v", err)
	}

	events := make(chan string, 4)
	if err := hub.Subscribe("rules", func(relPath string) { events <- relPath }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 删除外挂且内嵌缺失 → 重读失败，保留旧内容、不通知
	if err := os.Remove(filepath.Join(HotScriptsDir(), "rules", "x.txt")); err != nil {
		t.Fatal(err)
	}
	hub.pollOnce()
	got, err := hub.GetScriptText("rules", "x.txt")
	if err != nil {
		t.Fatalf("读失败后 GetScriptText 应返回旧内容: %v", err)
	}
	if got != "v1" {
		t.Fatalf("读失败后 = %q, want v1（保留旧内容）", got)
	}
	select {
	case rel := <-events:
		t.Fatalf("读失败不应通知订阅者: %q", rel)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestScriptHub_Register校验 空子目录/nil ScriptDir/重复注册均报错；Subscribe 未注册子目录报错。
func TestScriptHub_Register校验(t *testing.T) {
	hub, _ := hubTestEnv(t, nil)
	registerSub(hub, "rules", map[string]string{})

	if err := hub.Register("", NewScriptDir(fstest.MapFS{}, "x")); err == nil {
		t.Fatal("空子目录应报错")
	}
	if err := hub.Register("sql", nil); err == nil {
		t.Fatal("nil ScriptDir 应报错")
	}
	if err := hub.Register("rules", NewScriptDir(fstest.MapFS{}, "rules")); err == nil {
		t.Fatal("重复注册应报错")
	}
	if err := hub.Subscribe("nope", func(string) {}); err == nil {
		t.Fatal("Subscribe 未注册子目录应报错")
	}
	if err := hub.Subscribe("rules", nil); err == nil {
		t.Fatal("Subscribe nil 回调应报错")
	}
	if err := hub.Subscribe("rules", func(string) {}); err != nil {
		t.Fatalf("正常 Subscribe 应成功: %v", err)
	}
}

// TestScriptHub_多层目录递归 sql/ 下 mysql/postgres/sqlite 三层结构：递归扫描与读取。
func TestScriptHub_多层目录递归(t *testing.T) {
	hub, mk := hubTestEnv(t, nil)
	registerSub(hub, "sql", map[string]string{"mysql/a.sql": "embedded-mysql"})
	mk("sql", "mysql/a.sql", "ext-mysql")
	mk("sql", "sqlite/b.sql", "ext-sqlite")

	hub.pollOnce() // 递归扫描建立基线，无 panic

	got, err := hub.GetScriptText("sql", "sqlite/b.sql")
	if err != nil {
		t.Fatalf("GetScriptText: %v", err)
	}
	if got != "ext-sqlite" {
		t.Fatalf("三层读取 = %q, want ext-sqlite", got)
	}

	// 修改第三层文件触发
	mk("sql", "sqlite/b.sql", "v2")
	events := make(chan string, 4)
	if err := hub.Subscribe("sql", func(relPath string) { events <- relPath }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	hub.pollOnce()
	select {
	case rel := <-events:
		if rel != "sqlite/b.sql" {
			t.Fatalf("三层变化通知 rel = %q, want sqlite/b.sql", rel)
		}
	case <-time.After(time.Second):
		t.Fatal("三层文件修改未触发通知")
	}
}
