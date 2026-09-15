// dsn.go：外部数据源管理（管理面资源，供数据迁移按名/按 Code 引用外部库）。
//
//	GET  /admin/db/dsn        —— 列数据源（DSN 脱敏展示）
//	POST /admin/db/dsn        —— 添加 {name, driver, dsn[, test]}：驱动已注册 + Name 唯一
//	                           + DSN 重复拦截 + MySQL 密码 @ 预检（复用 easydb/dsn）
//	POST /admin/db/dsn/delete —— 按 {code} 删除（迁移任务进行中拒绝）
//	POST /admin/db/dsn/test   —— {driver, dsn} 连通测试（ping + 版本回显，不落盘）
//
// 存储：easydb/dsn 底座，JSON 文件 <CONF_DIR>/dsn.json（数据源是运行资产而非业务数据，不入业务库）。
// 每次读写实时取 CONF_DIR 当前生效值拼接路径（支持热更）；不使用 dsn.GetDsnConf
// 单例（其 sync.Once 锁死首次路径，与热更语义冲突）。懒创建：首次写入自动建目录，
// 读取时文件不存在视为空配置。变更 CONF_DIR 后原位置文件不自动搬迁（需手工移动）。
package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iotames/easydb/dsn"

	"rocksys/internal/taskcenter"
)

// 数据源管理端点路径。
const (
	PathDsn       = "/admin/db/dsn"
	PathDsnDelete = "/admin/db/dsn/delete"
	PathDsnTest   = "/admin/db/dsn/test"
)

// 支持的数据源驱动白名单（迁移目标库三方言）。
var dsnDrivers = map[string]bool{"sqlite": true, "mysql": true, "postgres": true}

// registerConfDir 注册全局配置目录项（adminapi.New 调用）：CONF_DIR 是全局统一配置目录，
func registerConfDir(confMgr interface {
	Register(ptr any, key, defVal, title string, usage ...string) error
}) (*string, error) {
	var confDir string
	if err := confMgr.Register(&confDir, "CONF_DIR", "conf",
		"全局配置目录（相对工作目录；外部数据源文件 dsn.json 存放于此）",
		"变更后原位置 dsn.json 不自动搬迁，需手工移动，否则按新位置视为空配置"); err != nil {
		return nil, err
	}
	return &confDir, nil
}

// confDirPath 拼接 <CONF_DIR 当前生效值>/dsn.json（每次读写实时取值，支持配置热更）。
func (s *AdminServer) confDirPath() string {
	dir := "conf"
	if s.confDir != nil && *s.confDir != "" {
		dir = *s.confDir
	}
	return filepath.Join(dir, "dsn.json")
}

// loadDsnGroup 读数据源组：文件不存在视为空配置（懒创建语义，不报错）。
func (s *AdminServer) loadDsnGroup() (dsn.DsnGroup, error) {
	var g dsn.DsnGroup
	fpath := s.confDirPath()
	if _, err := os.Stat(fpath); os.IsNotExist(err) {
		return g, nil
	}
	if err := dsn.NewDsnConf(fpath).GetDsnGroup(&g); err != nil {
		return g, fmt.Errorf("读取数据源配置 %s 失败: %w", fpath, err)
	}
	return g, nil
}

// saveDsnGroup 写数据源组：首次写入自动创建目录与文件（懒创建）。
func (s *AdminServer) saveDsnGroup(g dsn.DsnGroup) error {
	fpath := s.confDirPath()
	if dir := filepath.Dir(fpath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
		}
	}
	if err := dsn.NewDsnConf(fpath).SaveDsnGroup(g); err != nil {
		return fmt.Errorf("写入数据源配置 %s 失败: %w", fpath, err)
	}
	return nil
}

// getDsnByCode 按 Code 取数据源（迁移/结构对齐按 Code 引用，供 migrate.go 复用）。
func (s *AdminServer) getDsnByCode(code string) (dsn.DataSource, bool) {
	g, err := s.loadDsnGroup()
	if err != nil {
		return dsn.DataSource{}, false
	}
	for _, ds := range g.DsnList {
		if ds.Code == code {
			return ds, true
		}
	}
	return dsn.DataSource{}, false
}

// maskDsn DSN 脱敏：隐去凭据段，保留 user@host/db 形态摘要（连接串含明文密码，列表展示必须打码）。
// sqlite 为文件路径不含凭据，原样返回。
func maskDsn(driver, d string) string {
	switch driver {
	case "mysql":
		// user:pass@tcp(host:port)/db → user:***@tcp(host:port)/db
		// 密码可能含转义字符，从右往左找最后一个 @network( 分隔符之前的凭据段整体打码。
		if i := strings.Index(d, "://"); i >= 0 { // 非典型 URI 形态兜底
			return maskURIDsn(d, i)
		}
		if at := strings.LastIndex(d, "@"); at > 0 {
			if colon := strings.Index(d, ":"); colon > 0 && colon < at {
				return d[:colon+1] + "***" + d[at:]
			}
		}
		return d
	case "postgres":
		// postgres://user:pass@host/db → postgres://user:***@host/db；
		// key=value 形态打码 password= 值。
		if i := strings.Index(d, "://"); i >= 0 {
			return maskURIDsn(d, i)
		}
		return maskKVPassword(d)
	default:
		return d
	}
}

// maskURIDsn 打码 URI 形态 DSN 中 "scheme://" 之后的 user:password@ 凭据段。
func maskURIDsn(d string, schemeEnd int) string {
	rest := d[schemeEnd+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return d
	}
	cred := rest[:at]
	colon := strings.LastIndex(cred, ":")
	if colon < 0 {
		return d
	}
	return d[:schemeEnd+3] + cred[:colon+1] + "***" + rest[at:]
}

// maskKVPassword 打码 key=value 形态 DSN 的 password 值。
func maskKVPassword(d string) string {
	fields := strings.Fields(d)
	for i, f := range fields {
		if strings.HasPrefix(strings.ToLower(f), "password=") {
			fields[i] = "password=***"
		}
	}
	return strings.Join(fields, " ")
}

// handleDsnList 列数据源（DSN 脱敏）。
func (s *AdminServer) handleDsnList(w http.ResponseWriter, r *http.Request) {
	g, err := s.loadDsnGroup()
	if err != nil {
		http.Error(w, "数据源列表读取失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(g.DsnList))
	for _, ds := range g.DsnList {
		items = append(items, map[string]any{
			"code":   ds.Code,
			"name":   ds.Name,
			"driver": ds.DriverName,
			"dsn":    maskDsn(ds.DriverName, ds.Dsn),
		})
	}
	_ = writeJSON(w, map[string]any{"items": items}, http.StatusOK)
}

// handleDsnAdd 添加数据源：全链校验后持久化（可选 test=true 先连通测试）。
func (s *AdminServer) handleDsnAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
		Dsn    string `json:"dsn"`
		Test   bool   `json:"test"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "请求体须为 {\"name\",\"driver\",\"dsn\"} JSON", http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Driver = strings.ToLower(strings.TrimSpace(body.Driver))
	body.Dsn = strings.TrimSpace(body.Dsn)
	if body.Name == "" || body.Driver == "" || body.Dsn == "" {
		http.Error(w, "name/driver/dsn 均不能为空；driver 取 sqlite/mysql/postgres 之一", http.StatusBadRequest)
		return
	}
	if !dsnDrivers[body.Driver] {
		http.Error(w, "不支持的数据源驱动 "+body.Driver+"（仅支持 sqlite/mysql/postgres 三方言）", http.StatusBadRequest)
		return
	}
	// 驱动须已注册（未 import 的驱动 sql.Open 必失败，提前拦截给指引）。
	registered := false
	for _, dv := range sql.Drivers() {
		if dv == body.Driver {
			registered = true
			break
		}
	}
	if !registered {
		http.Error(w, "驱动 "+body.Driver+" 未注册，请确认构建包含该驱动后重试", http.StatusBadRequest)
		return
	}
	if body.Test {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if _, err := pingVersion(ctx, body.Driver, body.Dsn); err != nil {
			http.Error(w, "连通测试失败："+err.Error()+"；请核对连接串后重试，未保存任何配置", http.StatusBadRequest)
			return
		}
	}
	g, err := s.loadDsnGroup()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if g.HasName(body.Name) {
		http.Error(w, "连接名「"+body.Name+"」已存在，请换名或先删除同名数据源", http.StatusConflict)
		return
	}
	// AppendNamedDsn 内建：DSN 重复拦截（同 Code）+ MySQL 密码裸 @ 预检（驱动已校验）。
	if err := g.AppendNamedDsn(body.Name, body.Driver, body.Dsn); err != nil {
		http.Error(w, "添加数据源失败："+err.Error(), http.StatusConflict)
		return
	}
	if err := s.saveDsnGroup(g); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	added, _ := g.GetDSNByName(body.Name)
	_ = writeJSON(w, map[string]any{"ok": true, "code": added.Code}, http.StatusOK)
}

// handleDsnDelete 按 Code 删除：迁移任务进行中拒绝（防删除正在使用的目标源导致任务中途失联）。
func (s *AdminServer) handleDsnDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.Code == "" {
		http.Error(w, "请求体须为 {\"code\": \"...\"}", http.StatusBadRequest)
		return
	}
	for _, t := range s.tasks.List() {
		if t.Status == taskcenter.StatusRunning && (t.CreatedBy == "migrate" || t.CreatedBy == "schema_apply") {
			http.Error(w, "有数据迁移/结构对齐任务正在进行中（#"+t.ID+"「"+t.Title+"」），暂不能删除数据源；请等任务结束或先取消任务", http.StatusConflict)
			return
		}
	}
	g, err := s.loadDsnGroup()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	removed := false
	list := g.DsnList[:0]
	for _, ds := range g.DsnList {
		if ds.Code == body.Code {
			removed = true
			continue
		}
		list = append(list, ds)
	}
	if !removed {
		http.Error(w, "数据源不存在（Code="+body.Code+"）：请刷新列表确认最新状态", http.StatusNotFound)
		return
	}
	g.DsnList = list
	if g.ActiveCode == body.Code {
		g.ActiveCode = ""
		if len(g.DsnList) > 0 {
			g.ActiveCode = g.DsnList[0].Code
		}
	}
	if err := s.saveDsnGroup(g); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = writeJSON(w, map[string]any{"ok": true}, http.StatusOK)
}

// handleDsnTest 连通测试：sql.Open + Ping + 版本回显（不落盘，可测试未保存的 DSN）。
func (s *AdminServer) handleDsnTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Driver string `json:"driver"`
		Dsn    string `json:"dsn"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "请求体须为 {\"driver\",\"dsn\"} JSON", http.StatusBadRequest)
		return
	}
	body.Driver = strings.ToLower(strings.TrimSpace(body.Driver))
	if !dsnDrivers[body.Driver] {
		http.Error(w, "不支持的数据源驱动 "+body.Driver+"（仅支持 sqlite/mysql/postgres 三方言）", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	version, err := pingVersion(ctx, body.Driver, body.Dsn)
	if err != nil {
		http.Error(w, "连通测试失败："+err.Error()+"；请核对连接串（驱动、地址、账号密码、库名）后重试", http.StatusBadRequest)
		return
	}
	_ = writeJSON(w, map[string]any{"ok": true, "version": version}, http.StatusOK)
}

// pingVersion 打开连接、ping 并查询版本号（sqlite 无版本服务端，返回驱动名说明）。
func pingVersion(ctx context.Context, driver, dsnStr string) (string, error) {
	dbh, err := sql.Open(driver, dsnStr)
	if err != nil {
		return "", err
	}
	defer dbh.Close()
	if err := dbh.PingContext(ctx); err != nil {
		return "", err
	}
	var version string
	switch driver {
	case "mysql":
		if err := dbh.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
			return "", err
		}
	case "postgres":
		if err := dbh.QueryRowContext(ctx, "SHOW server_version").Scan(&version); err != nil {
			return "", err
		}
	default:
		if err := dbh.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
			version = "sqlite（驱动内置，无服务端版本）"
		}
	}
	return version, nil
}
