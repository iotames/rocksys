package geoip

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeDB 假 reader：按库名返回固定结果，用于覆盖分支而不依赖二进制 fixture。
type fakeDB struct {
	province            string
	code, country, city string
}

func (f *fakeDB) lookup(netip.Addr) GeoInfo {
	return GeoInfo{Code: f.code, Country: f.country, Province: f.province, City: f.city}
}

// newTestResolver 构造注入假 factory 的 Resolver；home 指向指定目录以隔离真实 $HOME。
// 返回 factory 的调用计数指针，用于断言"缺失结论被缓存、不再重复扫盘/打开"。
func newTestResolver(cfgDir, home string, files map[string]dbHandle) (*Resolver, *int) {
	calls := 0
	r := NewResolver(cfgDir)
	r.homeFn = func() (string, error) { return home, nil }
	r.factory = func(path string) (dbHandle, error) {
		calls++
		if h, ok := files[path]; ok {
			return h, nil
		}
		return nil, errors.New("fixture 未注册该路径")
	}
	return r, &calls
}

// writeEmpty 建空目录并返回路径。
func writeEmpty(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLookupSplitDirs 验证查找链逐文件解析：City 与 Country 分布在不同目录也能各自命中。
func TestLookupSplitDirs(t *testing.T) {
	dirA := writeEmpty(t)
	dirB := writeEmpty(t)
	// City 放在 $HOME/geoip（dirA 充当 HOME），Country 放在配置目录（dirB），验证逐文件跨目录命中。
	homeGeoip := filepath.Join(dirA, "geoip")
	if err := os.MkdirAll(homeGeoip, 0o755); err != nil {
		t.Fatal(err)
	}
	cityPath := filepath.Join(homeGeoip, CityFile)
	countryPath := filepath.Join(dirB, CountryFile)
	// 模拟"文件存在"：注册到 factory 的路径由 find 链决定，须真实放置文件才能被 Stat 命中。
	for _, p := range []string{cityPath, countryPath} {
		if err := os.WriteFile(p, []byte("mmdb"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 配置目录设为 dirB：Country 在配置目录命中，City 回落到 $HOME/geoip(dirA) 命中。
	r, calls := newTestResolver(dirB, dirA, map[string]dbHandle{
		cityPath:    &fakeDB{code: "CN", province: "广东省", city: "深圳市"},
		countryPath: &fakeDB{code: "CN"},
	})
	if !r.Ready() {
		t.Fatal("两库分别命中后 Ready 应为 true")
	}
	gi := r.Lookup("8.8.8.8")
	if gi.Code != "CN" || gi.Province != "广东省" || gi.City != "深圳市" {
		t.Fatalf("期望 CN/广东省/深圳市，实际 %q/%q/%q", gi.Code, gi.Province, gi.City)
	}
	if *calls != 2 {
		t.Fatalf("两库各打开一次，期望 factory 调用 2 次，实际 %d", *calls)
	}
}

// TestMissingDegrade 缺失降级：空目录下 Lookup 返回空串、不 panic、Ready 为 false，
// 且缺失结论被缓存——二次查询不再触发 factory（等价于不再扫盘打开）。
func TestMissingDegrade(t *testing.T) {
	dir := writeEmpty(t)
	r, calls := newTestResolver(dir, dir, nil)
	for i := 0; i < 3; i++ {
		if gi := r.Lookup("8.8.8.8"); !gi.empty() {
			t.Fatalf("缺失库应返回零值，实际 %+v", gi)
		}
	}
	if r.Ready() {
		t.Fatal("空目录下 Ready 应为 false")
	}
	if *calls != 0 {
		t.Fatalf("缺失结论应缓存（不重复打开），期望 0 次调用，实际 %d", *calls)
	}
}

// TestInvalidAndPrivateIP 非法/私有/回环 IP 一律空串，不走库。
func TestInvalidAndPrivateIP(t *testing.T) {
	dirA := writeEmpty(t)
	cityPath := filepath.Join(dirA, CityFile)
	if err := os.WriteFile(cityPath, []byte("mmdb"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, calls := newTestResolver(dirA, dirA, map[string]dbHandle{
		cityPath: &fakeDB{code: "CN", province: "广东省", city: "深圳市"},
	})
	for _, ip := range []string{"", "not-an-ip", "192.168.1.1", "127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "fe80::1"} {
		if gi := r.Lookup(ip); !gi.empty() {
			t.Fatalf("IP %q 应返回零值，实际 %+v", ip, gi)
		}
	}
	if *calls != 0 {
		t.Fatalf("私有/非法 IP 不应触库，期望 0 次调用，实际 %d", *calls)
	}
}

// TestOpenFailureCached 文件存在但打开失败：同样缓存失败结论，只尝试一次。
func TestOpenFailureCached(t *testing.T) {
	dir := writeEmpty(t)
	path := filepath.Join(dir, CountryFile)
	if err := os.WriteFile(path, []byte("not-a-mmdb"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, calls := newTestResolver(dir, dir, nil) // factory 未注册 → 打开失败
	for i := 0; i < 2; i++ {
		if gi := r.Lookup("8.8.8.8"); !gi.empty() {
			t.Fatalf("打开失败应返回零值，实际 %+v", gi)
		}
	}
	if *calls != 1 {
		t.Fatalf("失败结论应缓存（只尝试一次），期望 1 次调用，实际 %d", *calls)
	}
}

// TestCountryFallbackWhenCityMisses City 库无记录时回落 Country 库取国家码。
func TestCountryFallbackWhenCityMisses(t *testing.T) {
	dir := writeEmpty(t)
	cityPath := filepath.Join(dir, CityFile)
	countryPath := filepath.Join(dir, CountryFile)
	for _, p := range []string{cityPath, countryPath} {
		if err := os.WriteFile(p, []byte("mmdb"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := newTestResolver(dir, dir, map[string]dbHandle{
		cityPath:    &fakeDB{},
		countryPath: &fakeDB{code: "JP"},
	})
	gi := r.Lookup("8.8.8.8")
	if gi.Code != "JP" || gi.City != "" {
		t.Fatalf("期望回落 JP/空，实际 %q/%q", gi.Code, gi.City)
	}
}

// TestConcurrentLookup 并发 Lookup 不 data race、不 panic（配合 -race 运行）。
func TestConcurrentLookup(t *testing.T) {
	dir := writeEmpty(t)
	path := filepath.Join(dir, CityFile)
	if err := os.WriteFile(path, []byte("mmdb"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := newTestResolver(dir, dir, map[string]dbHandle{
		path: &fakeDB{country: "CN", province: "广东省", city: "深圳市"},
	})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Lookup("8.8.8.8")
			r.Ready()
		}()
	}
	wg.Wait()
}

// TestFirstSubdivision 一级行政区取值：取首个非空本地化名（zh-CN 优先），无则空串。
func TestFirstSubdivision(t *testing.T) {
	cases := []struct {
		subs []nameMap
		want string
	}{
		{[]nameMap{{map[string]string{"zh-CN": "广东省", "en": "Guangdong"}}}, "广东省"},
		{[]nameMap{{map[string]string{"en": "California"}}}, "California"},
		{[]nameMap{{map[string]string{"en": ""}}, {map[string]string{"en": "Texas"}}}, "Texas"},
		{[]nameMap{}, ""},
	}
	for i, c := range cases {
		if got := firstSubdivision(c.subs); got != c.want {
			t.Fatalf("用例 %d：期望 %q，实际 %q", i, c.want, got)
		}
	}
}

// TestEnabledSwitch 功能开关语义（GEOIP_ENABLED 内聚裁决点，GEOIP_SWITCH D1/D2）：
//   - 默认开启；禁用后 Enabled/Ready 为 false、Lookup 恒零值，且不再触发库加载（扫盘短路）；
//   - 重新开启后恢复：once 已缓存的库直接可用，无需重新打开。
func TestEnabledSwitch(t *testing.T) {
	dir := writeEmpty(t)
	path := filepath.Join(dir, CityFile)
	if err := os.WriteFile(path, []byte("mmdb"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, calls := newTestResolver(dir, dir, map[string]dbHandle{
		path: &fakeDB{code: "CN", province: "广东省", city: "深圳市"},
	})
	if !r.Enabled() {
		t.Fatal("默认应开启")
	}
	if !r.Ready() {
		t.Fatal("开启且库就绪时 Ready 应为 true")
	}
	loaded := *calls // 首次 Ready 已触发一次惰性加载
	r.SetEnabled(false)
	if r.Enabled() || r.Ready() {
		t.Fatal("禁用后 Enabled/Ready 应为 false")
	}
	if gi := r.Lookup("8.8.8.8"); !gi.empty() {
		t.Fatalf("禁用后 Lookup 应返回零值，实际 %+v", gi)
	}
	_ = r.Ready() // 禁用期间的就绪判定应短路，不得再触发库加载
	if *calls != loaded {
		t.Fatalf("禁用期间 Ready 不应触发库加载，期望 %d 次调用，实际 %d", loaded, *calls)
	}
	r.SetEnabled(true)
	if !r.Ready() {
		t.Fatal("重新开启后 Ready 应恢复 true（once 缓存命中）")
	}
	if gi := r.Lookup("8.8.8.8"); gi.Code != "CN" || gi.City != "深圳市" {
		t.Fatalf("重新开启后 Lookup 应恢复，实际 %+v", gi)
	}
	if *calls != loaded {
		t.Fatalf("重新开启后不应重复打开库（once 缓存），期望 %d 次调用，实际 %d", loaded, *calls)
	}
}

// TestSyncPathBypassesSwitch 手动同步路径（SyncReady/LookupSync）不受开关限制：
// GEOIP_ENABLED=false 时 SyncReady 仍为 true、LookupSync 照常解析——手动同步是
// 特殊场景的异步 DB 维护任务，允许在功能关闭时单独操作补齐 geoip_list 关联表。
func TestSyncPathBypassesSwitch(t *testing.T) {
	dir := writeEmpty(t)
	path := filepath.Join(dir, CityFile)
	if err := os.WriteFile(path, []byte("mmdb"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := newTestResolver(dir, dir, map[string]dbHandle{
		path: &fakeDB{code: "CN", province: "广东省", city: "深圳市"},
	})
	r.SetEnabled(false)
	if !r.SyncReady() {
		t.Fatal("开关关闭但 mmdb 已加载时 SyncReady 应为 true")
	}
	if r.Ready() {
		t.Fatal("开关关闭时 Ready 应为 false")
	}
	if gi := r.LookupSync("8.8.8.8"); gi.Code != "CN" || gi.City != "深圳市" {
		t.Fatalf("开关关闭时 LookupSync 应照常解析，实际 %+v", gi)
	}
	if gi := r.Lookup("8.8.8.8"); !gi.empty() {
		t.Fatalf("开关关闭时 Lookup 仍应返回零值（读侧降级），实际 %+v", gi)
	}
}
