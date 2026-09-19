// Package geoip 提供基于 GeoLite2 mmdb 文件的 IP 地理位置解析。
//
// 设计要点（docs/plan/TRAFFIC_ANALYSIS_PLAN.md §3.3/D12/D15/D17）：
//   - 服务提供者模式：功能开关（GEOIP_ENABLED）内聚在本包 Resolver 上裁决，
//     消费方只经 Ready/Enabled/Lookup 少数入口提问，不各自读配置——禁用语义单点收敛。
//     手动同步是例外（特殊异步 DB 维护任务）：经 SyncReady/LookupSync 不受开关限制。
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

	// DisabledText 功能禁用（GEOIP_ENABLED=false）时读侧展示的规范替代文案：
	// 由提供者单点定义，消费方（明细回填/Top IP 等）直接引用，保证全站口径一致。
	// 只用于展示，绝不落库——写侧同步被 Ready() 门控阻断，禁用期间不会产生新写入。
	DisabledText = "服务未开启"
)

// nameMap mmdb 的多语言名称字典（zh-CN/en 等）。
type nameMap struct {
	Names map[string]string `maxminddb:"names"`
}

// geoRecord 只声明需要的字段，避免整棵 GeoIP2 记录树的解码开销（热路径）。
// 注意：Country.Names 必须直写 map[string]string——经嵌套包装结构体间接承接时
// maxminddb-golang v2 解码结果为空 map（省/市与数组元素形态均正常，实测 2026-09-15）。
type geoRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []nameMap `maxminddb:"subdivisions"`
	City         nameMap   `maxminddb:"city"`
}

// GeoInfo 一次查询的完整地理信息（名实相符、省与市分离）：
//   - Code     ISO 国家码（如 "CN"，聚合口径）
//   - Country  国名（本地化优先 zh-CN，缺失回落 en，如 "中国"；无则为空）
//   - Province 一级行政区全称（zh-CN 优先，如 "广东省"/"California"；中国地图着色依赖全称）
//   - City     城市名（仅市，如 "深圳市"；不再与省拼接）
//
// 任何失败路径字段均为空串（零值可用）。
type GeoInfo struct {
	Code     string
	Country  string
	Province string
	City     string
}

// empty 判断是否无有效地理信息。
func (g GeoInfo) empty() bool {
	return g.Code == "" && g.Country == "" && g.Province == "" && g.City == ""
}

// dbHandle 抽象单个 mmdb 库的查询能力，便于单测注入假 reader 覆盖分支，
// 无需往仓库提交二进制 fixture（真实文件路径走环境变量门控的集成测试）。
type dbHandle interface {
	// lookup 返回该库能提供的地理信息；记录不存在时返回零值。
	lookup(ip netip.Addr) GeoInfo
}

// dbFactory 按路径打开一个 mmdb 库；独立成函数类型是为了测试时替换。
type dbFactory func(path string) (dbHandle, error)

// maxmindDB 基于真实 maxminddb-golang v2 Reader 的 dbHandle 实现。
// Reader 本身线程安全且查询在内存完成，无需再加锁或 LRU。
type maxmindDB struct {
	r *maxminddb.Reader
}

func (m *maxmindDB) lookup(ip netip.Addr) GeoInfo {
	var rec geoRecord
	if err := m.r.Lookup(ip).Decode(&rec); err != nil {
		// 无记录或字段缺失是常态（如仅 Country 库查省市），静默返回零值即可。
		return GeoInfo{}
	}
	return GeoInfo{
		Code:     rec.Country.ISOCode,
		Country:  pickName(rec.Country.Names),
		Province: firstSubdivision(rec.Subdivisions),
		City:     pickName(rec.City.Names),
	}
}

// pickName 名称字典取值：优先中文，缺失回落英文（部分区域无 zh-CN 译名）。
func pickName(m map[string]string) string {
	if v := m["zh-CN"]; v != "" {
		return v
	}
	return m["en"]
}

// firstSubdivision 取首个一级行政区（省/州）本地化名称；无则空串。
// 值以 mmdb zh-CN 实际返回为准（多为短名如「广东」，部分为全称如「北京市」）；
// 前端中国地图对全称经 chinaShort 映射、短名与 geojson 直连，两者均正确着色。
func firstSubdivision(subs []nameMap) string {
	for _, s := range subs {
		if name := pickName(s.Names); name != "" {
			return name
		}
	}
	return ""
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

	enabled atomic.Bool // 功能总开关（GEOIP_ENABLED 热更注入；false=服务未开启，禁用语义唯一裁决点）

	city    lazyDB
	country lazyDB
}

// NewResolver 构造解析器。dir 为数据目录（空串回落 DefaultDir="geoip"）。
// 功能默认开启（GEOIP_ENABLED 缺省 true），装配层经 SetEnabled 注入配置实际值。
// 日志沿用仓库统一的 easyserver/log 包级 API，与既有插件风格一致。
func NewResolver(dir string) *Resolver {
	if dir == "" {
		dir = DefaultDir
	}
	r := &Resolver{
		cfgDir:  dir,
		factory: openMaxmind,
		homeFn:  os.UserHomeDir,
	}
	r.enabled.Store(true)
	return r
}

// SetEnabled 设置功能总开关（装配层经配置热更回调注入，运行期改值即时生效）。
func (r *Resolver) SetEnabled(v bool) { r.enabled.Store(v) }

// Enabled 功能是否开启（GEOIP_ENABLED 当前值）。
func (r *Resolver) Enabled() bool { return r.enabled.Load() }

// Ready GeoIP 服务是否就绪（= 功能开启 且 至少成功加载一个库），供装配门控与
// 前端 geo 状态检测。禁用即未就绪：自动定时同步不启动、读侧降级、日程登记降级，
// 全部既有门控经本入口自动遵守开关，无需各自感知配置。
// 首次调用会触发一次惰性定位（同样受 once 缓存，后续调用零开销）；
// 禁用时短路返回，不触发扫盘。
func (r *Resolver) Ready() bool {
	if !r.Enabled() {
		return false
	}
	return r.SyncReady()
}

// SyncReady 手动同步是否可执行（= 至少成功加载一个库，不受 GEOIP_ENABLED 限制）。
// 手动同步定位为特殊场景的数据库维护任务：异步后台执行、不影响主程序转发，
// 即使功能开关关闭（实时解析停用）也允许单独操作补齐 geoip_list 关联表。
// 首次调用触发一次惰性定位（once 缓存，后续零开销）。
func (r *Resolver) SyncReady() bool {
	return r.city.load(r, CityFile) != nil || r.country.load(r, CountryFile) != nil
}

// Lookup 返回地理信息（ISO 码、本地化国名、省市）。任何失败路径（功能禁用、非法 IP、
// 私有/回环地址、库缺失、解析失败）一律返回零值，绝不 panic、绝不阻断转发主流程。
// 禁用时返回零值而非"服务未开启"标记——本包不把展示文案混进数据结构，
// 读侧展示层按 Enabled() 决定以 DisabledText 替代，写侧（同步落库）天然安全跳过。
func (r *Resolver) Lookup(ipStr string) GeoInfo {
	if !r.Enabled() {
		return GeoInfo{}
	}
	return r.LookupSync(ipStr)
}

// LookupSync 与 Lookup 同语义，但不受 GEOIP_ENABLED 限制——仅供 geoip_list 同步
// 任务（特殊场景的异步 DB 维护操作，cmd/rocksys/geoip_sync.go）使用：功能关闭时
// 仍可单独手动同步补齐关联表。读侧实时路径禁止使用本入口（统一走 Lookup）。
func (r *Resolver) LookupSync(ipStr string) GeoInfo {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil || !ip.IsValid() {
		return GeoInfo{}
	}
	// 私有/回环/链路本地等地址无地理意义，提前短路，也避免误命中库里的保留段。
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return GeoInfo{}
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
	}

	// 合并双库：字段级取非空优先（Country 库可能只有国名，City 库省市更细；
	// 同字段两库都有值时 City 库优先，避免两库不一致）。
	info := GeoInfo{}
	if c := r.country.load(r, CountryFile); c != nil {
		info = (*c).lookup(ip)
	}
	if c := r.city.load(r, CityFile); c != nil {
		if ci := (*c).lookup(ip); !ci.empty() {
			if info.Code == "" {
				info.Code = ci.Code
			}
			if info.Country == "" {
				info.Country = ci.Country
			}
			if info.Province == "" {
				info.Province = ci.Province
			}
			info.City = ci.City
		}
	}
	// 注意：这里不做"市空退省/省空退国"的兜底——country/city 列保持解析真实语义（该空则空），
	// 兜底只做在读侧最终展示（traffic 读端点映射），避免污染数据层口径。
	return info
}

// load 惰性定位并加载单个库（once 幂等）；缺失或打开失败缓存 nil 并告警一次。
func (l *lazyDB) load(r *Resolver, name string) *dbHandle {
	l.once.Do(func() {
		path := r.find(name)
		if path == "" {
			log.Warn("geoip: 未找到数据库文件，地理信息将留空",
				"file", name,
				"search_dirs", r.searchDirs(),
				"hint", "下载 GeoLite2 库放入上述任一目录后重启生效；直链参考（P3TERX/GeoLite.mmdb，latest 恒指现存最新版）：",
				"dl_city", "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb",
				"dl_country", "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-Country.mmdb")
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
