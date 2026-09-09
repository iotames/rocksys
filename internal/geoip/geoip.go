// Package geoip 提供基于 GeoLite2 mmdb 文件的 IP 地理位置解析。
//
// 设计要点（docs/plan/TRAFFIC_ANALYSIS_PLAN.md §3.3/D12/D15/D17）：
//   - 惰性加载：首次 Lookup 才定位加载，"缺失"结论同样缓存，避免热路径反复扫盘；
//     文件补放后重启生效（不做热加载、不做文件监控），重启后 once 重建自然重新查找。
//   - 查找链逐文件独立：City 与 Country 库各自按 配置目录 → 当前工作目录 → $HOME/geoip
//     定位，首个含该文件的目录胜出，两文件允许分布在不同目录。
//   - 绝不阻断主流程：解析失败、私有 IP、未加载一律返回空串，geo 只是增强信息而非转发前提。
package geoip

import (
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/iotames/easyserver/log"
)

const (
	// CityFile / CountryFile GeoLite2 数据库文件名，与 MaxMind 官方发布名一致。
	CityFile    = "GeoLite2-City.mmdb"
	CountryFile = "GeoLite2-Country.mmdb"

	// DefaultDir 默认数据目录（相对工作目录解析），与 hotscripts 等外挂资源同级约定。
	DefaultDir = "geoip"
)

// nameMap mmdb 的多语言名称字典（zh-CN/en 等）。
type nameMap struct {
	Names map[string]string `maxminddb:"names"`
}

// geoRecord 只声明需要的字段，避免整棵 GeoIP2 记录树的解码开销（热路径）。
type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []nameMap `maxminddb:"subdivisions"`
	City         nameMap   `maxminddb:"city"`
}

// dbHandle 抽象单个 mmdb 库的查询能力，便于单测注入假 reader 覆盖分支，
// 无需往仓库提交二进制 fixture（真实文件路径走环境变量门控的集成测试）。
type dbHandle interface {
	// lookup 返回 (ISO 国家码, 省市拼接)；记录不存在时返回空串。
	lookup(ip netip.Addr) (country, city string)
}

// dbFactory 按路径打开一个 mmdb 库；独立成函数类型是为了测试时替换。
type dbFactory func(path string) (dbHandle, error)

// maxmindDB 基于真实 maxminddb-golang v2 Reader 的 dbHandle 实现。
// Reader 本身线程安全且查询在内存完成，无需再加锁或 LRU。
type maxmindDB struct {
	r *maxminddb.Reader
}

func (m *maxmindDB) lookup(ip netip.Addr) (country, city string) {
	var rec geoRecord
	if err := m.r.Lookup(ip).Decode(&rec); err != nil {
		// 无记录或字段缺失是常态（如仅 Country 库查省市），静默返回空即可。
		return "", ""
	}
	return rec.Country.ISOCode, joinNames(rec.Subdivisions, rec.City)
}

// joinNames 拼"省/市"，名字优先中文，其次英文，避免空段产生多余分隔符。
func joinNames(subs []nameMap, city nameMap) string {
	pick := func(m nameMap) string {
		if v := m.Names["zh-CN"]; v != "" {
			return v
		}
		return m.Names["en"]
	}
	out := ""
	for _, s := range subs {
		if name := pick(s); name != "" {
			out += name + "/"
		}
	}
	if name := pick(city); name != "" {
		out += name
	}
	// 去掉末尾可能悬空的分隔符（有省无市的情形）。
	for len(out) > 0 && out[len(out)-1] == '/' {
		out = out[:len(out)-1]
	}
	return out
}

// lazyDB 单个库的惰性加载器：once 保证只定位一次，失败/缺失结论一并缓存。
// 加载后字段只读，Lookup 并发安全。
type lazyDB struct {
	once sync.Once
	db   atomic.Pointer[dbHandle] // nil 表示未加载或缺失，二者查询语义相同（返回空）
}

// Resolver GeoIP 解析器。零值不可用，须经 NewResolver 构造。
type Resolver struct {
	cfgDir  string // 配置的数据目录（构造传入，可为空走 DefaultDir）
	factory dbFactory
	homeFn  func() (string, error) // $HOME 获取函数，独立出来便于测试替换

	city    lazyDB
	country lazyDB
}

// NewResolver 构造解析器。dir 为数据目录（空串回落 DefaultDir="geoip"）。
// 日志沿用仓库统一的 easyserver/log 包级 API，与既有插件风格一致。
func NewResolver(dir string) *Resolver {
	if dir == "" {
		dir = DefaultDir
	}
	return &Resolver{
		cfgDir:  dir,
		factory: openMaxmind,
		homeFn:  os.UserHomeDir,
	}
}

// Ready 是否至少成功加载了一个库，供读侧/前端 geo 状态检测。
// 首次调用会触发一次惰性定位（同样受 once 缓存，后续调用零开销），
// 这样"文件已就位但尚未有流量"时状态检测也能给出正确结论。
func (r *Resolver) Ready() bool {
	return r.city.load(r, CityFile) != nil || r.country.load(r, CountryFile) != nil
}

// Lookup 返回 (ISO 国家码如 "CN", 省市拼接如 "广东省/深圳市")。
// 任何失败路径（非法 IP、私有/回环地址、库缺失、解析失败）一律返回空串，
// 绝不 panic、绝不阻断转发主流程。
func (r *Resolver) Lookup(ipStr string) (country, city string) {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil || !ip.IsValid() {
		return "", ""
	}
	// 私有/回环/链路本地等地址无地理意义，提前短路，也避免误命中库里的保留段。
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return "", ""
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
	}

	if c := r.country.load(r, CountryFile); c != nil {
		country, _ = (*c).lookup(ip)
	}
	// City 库优先：国家码与省市同源，避免两库不一致；Country 库作为 City 缺失时的兜底。
	if c := r.city.load(r, CityFile); c != nil {
		if cy, ct := (*c).lookup(ip); cy != "" || ct != "" {
			return cy, ct
		}
	}
	return country, ""
}

// load 惰性定位并加载单个库（once 幂等）；缺失或打开失败缓存 nil 并告警一次。
func (l *lazyDB) load(r *Resolver, name string) *dbHandle {
	l.once.Do(func() {
		path := r.find(name)
		if path == "" {
			log.Warn("geoip: 未找到数据库文件，地理信息将留空",
				"file", name,
				"search_dirs", r.searchDirs(),
				"hint", "下载 GeoLite2 库放入上述任一目录后重启生效；直链参考（P3TERX/GeoLite.mmdb）：",
				"dl_city", "https://github.com/P3TERX/GeoLite.mmdb/releases/download/2026.09.07/GeoLite2-City.mmdb",
				"dl_country", "https://github.com/P3TERX/GeoLite.mmdb/releases/download/2026.09.07/GeoLite2-Country.mmdb")
			return
		}
		h, err := r.factory(path)
		if err != nil {
			log.Warn("geoip: 数据库文件打开失败，地理信息将留空",
				"file", path, "err", err.Error(),
				"hint", "请检查文件完整性或重新下载 GeoLite2 库，替换后重启生效")
			return
		}
		l.db.Store(&h)
	})
	return l.db.Load()
}

// find 沿查找链定位文件：配置目录 → 当前工作目录 → $HOME/geoip，首个含该文件的目录胜出。
func (r *Resolver) find(name string) string {
	for _, dir := range r.searchDirs() {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// searchDirs 返回查找链目录列表。相对目录按相对工作目录解析（与外挂资源约定一致）。
func (r *Resolver) searchDirs() []string {
	dirs := []string{r.cfgDir, "."}
	if home, err := r.homeFn(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "geoip"))
	}
	return dirs
}

// openMaxmind 打开真实 mmdb 文件。
func openMaxmind(path string) (dbHandle, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &maxmindDB{r: reader}, nil
}
