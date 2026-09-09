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
	country, city string
}

func (f *fakeDB) lookup(netip.Addr) (string, string) { return f.country, f.city }

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
		cityPath:    &fakeDB{country: "CN", city: "广东省/深圳市"},
		countryPath: &fakeDB{country: "CN"},
	})
	if !r.Ready() {
		t.Fatal("两库分别命中后 Ready 应为 true")
	}
	country, city := r.Lookup("8.8.8.8")
	if country != "CN" || city != "广东省/深圳市" {
		t.Fatalf("期望 CN/广东省/深圳市，实际 %q/%q", country, city)
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
		country, city := r.Lookup("8.8.8.8")
		if country != "" || city != "" {
			t.Fatalf("缺失库应返回空串，实际 %q/%q", country, city)
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
		cityPath: &fakeDB{country: "CN", city: "广东省/深圳市"},
	})
	for _, ip := range []string{"", "not-an-ip", "192.168.1.1", "127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "fe80::1"} {
		country, city := r.Lookup(ip)
		if country != "" || city != "" {
			t.Fatalf("IP %q 应返回空串，实际 %q/%q", ip, country, city)
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
		if country, city := r.Lookup("8.8.8.8"); country != "" || city != "" {
			t.Fatalf("打开失败应返回空串，实际 %q/%q", country, city)
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
		countryPath: &fakeDB{country: "JP"},
	})
	country, city := r.Lookup("8.8.8.8")
	if country != "JP" || city != "" {
		t.Fatalf("期望回落 JP/空，实际 %q/%q", country, city)
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
		path: &fakeDB{country: "CN", city: "广东省/深圳市"},
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

// TestJoinNames 省市拼接：中文名优先、空段不产多余分隔符、无省市返回空。
func TestJoinNames(t *testing.T) {
	cases := []struct {
		subs []nameMap
		city nameMap
		want string
	}{
		{[]nameMap{{map[string]string{"zh-CN": "广东省", "en": "Guangdong"}}},
			nameMap{map[string]string{"zh-CN": "深圳市", "en": "Shenzhen"}}, "广东省/深圳市"},
		{[]nameMap{{map[string]string{"en": "California"}}},
			nameMap{map[string]string{"en": "Mountain View"}}, "California/Mountain View"},
		{[]nameMap{{map[string]string{"zh-CN": "广东省"}}}, nameMap{nil}, "广东省"},
		{[]nameMap{}, nameMap{nil}, ""},
	}
	for i, c := range cases {
		if got := joinNames(c.subs, c.city); got != c.want {
			t.Fatalf("用例 %d：期望 %q，实际 %q", i, c.want, got)
		}
	}
}
