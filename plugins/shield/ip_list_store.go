// 动态 IP 黑白名单数据访问层（sql/<dbtype>/ 外置 SQL，参数化占位符）。
//
// ★ 性能红线（WAF 方案 §5.3）：请求热路径零 DB 查询——本 store 仅服务
// 管理面 CRUD/导入、启动加载与后台 TTL 刷新/hit_count 攒批累加；
// 拦截链路只读 Shield 维护的内存快照，不触达本层。
//
// 表结构见 sql/<dbtype>/ip_blacklist_create_table.sql（黑）与
// ip_whitelist_create_table.sql（白）：ip 唯一约束保证重复导入幂等跳过；
// 软删除/过期语义由 SQL 过滤（deleted_at IS NULL 且未过期）承担。
package shield

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iotames/easydb"

	"rocksys/internal/db"
)

// ErrIPExists 新增时 ip 已存在（唯一约束冲突），由管理面按幂等语义处理。
var ErrIPExists = errors.New("ip 已存在")

// ErrIPNotExists 按 ip 查询无记录（封禁三态判定走"新增入库"分支）。
var ErrIPNotExists = errors.New("ip 记录不存在")

// ActiveIP 有效黑白名单条目（内存快照加载用：id 供 hit_count 累加，ip 供匹配）。
type ActiveIP struct {
	ID int64
	IP string
}

// ListFilter 管理面列表过滤条件（白名单忽略 BlockType/Sort）。
type ListFilter struct {
	IP        string    // ip 模糊匹配（空 = 不限）
	BlockType BlockType // 黑名单：0 = 不限（白名单忽略）
	ValidOnly bool      // true = 仅有效（未软删、未过期）；false = 全部（含软删/过期）
	Sort      string    // 黑名单排序键（白名单映射，见 blacklistSortWhitelist；非法/缺省回 id DESC）
	Limit     int       // 分页大小（<=0 回落默认 500，上限 10000）
	Offset    int       // 分页偏移
}

// IPListStore 动态 IP 黑白名单表数据访问（黑名单 isBlack=true，表 ip_blacklist）。
type IPListStore struct {
	edb     *easydb.EasyDb
	sqls    db.SQLSource
	table   string // "ip_blacklist" / "ip_whitelist"
	isBlack bool   // 黑名单专属列：block_type / hit_count / expires_at
}

// NewIPListStore 构造黑白名单 store。isBlack=true 为黑名单（表 ip_blacklist）。
// edb/sqls 均来自统一数据访问层（main.go 装配：dataDB.EasyDB() / dataDB）。
func NewIPListStore(edb *easydb.EasyDb, sqls db.SQLSource, isBlack bool) *IPListStore {
	tbl := "ip_whitelist"
	if isBlack {
		tbl = "ip_blacklist"
	}
	return &IPListStore{edb: edb, sqls: sqls, table: tbl, isBlack: isBlack}
}

// Table 返回表名（管理面/装配诊断用）。
func (s *IPListStore) Table() string { return s.table }

// sqlText 读取脚本并替换占位符：{table} 表名（代码固定值，非用户输入，安全）、
// {order} 排序表达式（黑名单 query_list 用，经排序白名单映射注入；其余脚本无此占位符，替换为空操作）。
func (s *IPListStore) sqlText(name string) (string, error) {
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("shield: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	txt = strings.ReplaceAll(txt, "{table}", s.table)
	return strings.ReplaceAll(txt, "{order}", blacklistSortOrder(defaultListSort)), nil
}

// sqlTextOrder 同 sqlText，但 {order} 由调用方传入排序键（经白名单映射，非用户输入直拼）。
func (s *IPListStore) sqlTextOrder(name, sort string) (string, error) {
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("shield: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	txt = strings.ReplaceAll(txt, "{table}", s.table)
	return strings.ReplaceAll(txt, "{order}", blacklistSortOrder(sort)), nil
}

// 排序白名单（IP_BLACKLIST_PLAN §3.8）：sort 参数 → ORDER BY 表达式。
// 仅数值/时间列开放且固定 DESC；字符串列（ip/title）不提供，杜绝拼接注入面。
const defaultListSort = "id"

var blacklistSortWhitelist = map[string]string{
	"id":         "id DESC",
	"hit_count":  "hit_count DESC",
	"warn_times": "warn_times DESC",
	"created_at": "created_at DESC",
	"expires_at": "expires_at DESC",
	"updated_at": "updated_at DESC",
	"block_type": "block_type DESC",
}

// blacklistSortOrder 排序键白名单映射：非法/缺省回默认 id DESC。
func blacklistSortOrder(sort string) string {
	if expr, ok := blacklistSortWhitelist[sort]; ok {
		return expr
	}
	return blacklistSortWhitelist[defaultListSort]
}

// EnsureAttackArchiveTable 幂等建攻击证据归档表（WAF 方案 §4.3：本期仅建表，
// 归档触发/查询逻辑留待后续迭代）。由 main.go 装配时调用；失败告警不阻断。
func EnsureAttackArchiveTable(edb *easydb.EasyDb, sqls db.SQLSource) error {
	const table = "attack_archive"
	ddl, err := sqls.SQL(table + "_create_table.sql")
	if err != nil {
		return err
	}
	if _, err := edb.Exec(strings.ReplaceAll(ddl, "{table}", table)); err != nil {
		return fmt.Errorf("shield: 建 %s 表失败: %w", table, err)
	}
	idx, err := sqls.SQL(table + "_create_index.sql")
	if err != nil {
		return err
	}
	for _, stmt := range db.SplitSQLStatements(strings.ReplaceAll(idx, "{table}", table)) {
		if _, err := edb.Exec(stmt); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
				continue
			}
			return fmt.Errorf("shield: 建 %s 表索引失败: %w", table, err)
		}
	}
	return nil
}

// scriptName 拼 SQL 文件名：文件名前缀固定（ip_blacklist/ip_whitelist，由 isBlack 决定），
// 与表名解耦——表名（s.table）可替换（测试隔离/未来配置化），文件名不受影响。
func (s *IPListStore) scriptName(action string) string {
	prefix := "ip_whitelist"
	if s.isBlack {
		prefix = "ip_blacklist"
	}
	return prefix + "_" + action + ".sql"
}

// EnsureTable 幂等建表 + 索引（失败不阻断启动，由调用方记录告警）。
func (s *IPListStore) EnsureTable() error {
	ddl, err := s.sqlText(s.scriptName("create_table"))
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(ddl); err != nil {
		return fmt.Errorf("shield: 建 %s 表失败: %w", s.table, err)
	}
	idx, err := s.sqlText(s.scriptName("create_index"))
	if err != nil {
		return err
	}
	// 多语句脚本逐条执行 + 幂等容错（MySQL 的 CREATE INDEX 无 IF NOT EXISTS）。
	for _, stmt := range db.SplitSQLStatements(idx) {
		if _, err := s.edb.Exec(stmt); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
				continue
			}
			return fmt.Errorf("shield: 建 %s 表索引失败: %w", s.table, err)
		}
	}
	return nil
}

// QueryActive 查询全部有效条目（未软删且未过期）供内存快照加载。
// ★ 黑名单按 expires_at 过滤需要 now 参数；白名单无过期列，不传参数
// （PG 对多余参数严格报错，sqlite 宽松——必须按方言语义区分）。
func (s *IPListStore) QueryActive(now time.Time) ([]ActiveIP, error) {
	sel, err := s.sqlText(s.scriptName("query_active"))
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if s.isBlack {
		err = s.edb.GetMany(sel, &rows, now.UTC())
	} else {
		err = s.edb.GetMany(sel, &rows)
	}
	out := make([]ActiveIP, 0, len(rows))
	for _, row := range rows {
		out = append(out, ActiveIP{ID: eventToInt64(row["id"]), IP: eventToString(row["ip"])})
	}
	return out, nil
}

// Insert 新增一条（普通录入/导入语义：warn_times 初始 0）。黑名单接受 blockType/expiresAt；
// 白名单忽略二者。ip 已存在（唯一约束冲突）返回 ErrIPExists。
// ★ PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，本方言走
// insert_returning_id.sql（RETURNING id + QueryRow.Scan），其余方言 Exec + LastInsertId
// （与 mq.OutboxStore.Insert 同模式）。
func (s *IPListStore) Insert(ip, title string, blockType BlockType, expiresAt *time.Time, now time.Time) (int64, error) {
	return s.insertWarnTimes(ip, title, blockType, expiresAt, 0, now)
}

// BanInsert 封禁入库（IP_BLACKLIST_PLAN §3.4 决策 8：自动/人工封禁入库 warn_times=1 起算）。
// 仅黑名单语义（白名单调用报错）；ip 已存在返回 ErrIPExists。
func (s *IPListStore) BanInsert(ip, title string, blockType BlockType, expiresAt *time.Time, now time.Time) (int64, error) {
	if !s.isBlack {
		return 0, fmt.Errorf("shield: 白名单无封禁语义（warn_times）")
	}
	return s.insertWarnTimes(ip, title, blockType, expiresAt, 1, now)
}

// insertWarnTimes 插入内部实现（warnTimes：封禁入库=1，普通录入/导入=0）。
func (s *IPListStore) insertWarnTimes(ip, title string, blockType BlockType, expiresAt *time.Time, warnTimes int, now time.Time) (int64, error) {
	nowUTC := now.UTC()
	bt := int(blockType)
	title = truncateTitle(title, 0)
	if !s.isBlack {
		bt, expiresAt = 1, nil // 白名单无 block_type/expires_at 列
	}
	if da, ok := s.sqls.(interface{ Driver() string }); ok && da.Driver() == "postgres" {
		ret, err := s.sqlText(s.scriptName("insert_returning_id"))
		if err != nil {
			return 0, err
		}
		var id int64
		var qerr error
		if s.isBlack {
			qerr = s.edb.QueryRow(ret, ip, title, bt, warnTimes, utcOrNil(expiresAt), nowUTC, nowUTC).Scan(&id)
		} else {
			qerr = s.edb.QueryRow(ret, ip, title, nowUTC, nowUTC).Scan(&id)
		}
		if qerr != nil {
			if isUniqueErr(qerr) {
				return 0, ErrIPExists
			}
			return 0, fmt.Errorf("shield: 新增 %s 失败（RETURNING id）: %w", s.table, qerr)
		}
		return id, nil
	}
	ins, err := s.sqlText(s.scriptName("insert"))
	if err != nil {
		return 0, err
	}
	var res sqlResult
	if s.isBlack {
		res, err = s.edb.Exec(ins, ip, title, bt, warnTimes, utcOrNil(expiresAt), nowUTC, nowUTC)
	} else {
		res, err = s.edb.Exec(ins, ip, title, nowUTC, nowUTC)
	}
	if err != nil {
		if isUniqueErr(err) {
			return 0, ErrIPExists
		}
		return 0, fmt.Errorf("shield: 新增 %s 失败: %w", s.table, err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// Update 更新条目。黑名单：title/blockType/expiresAt；白名单：仅 title。
// id 不存在（已被物理删除）时 RowsAffected=0，返回 nil（幂等）。
func (s *IPListStore) Update(id int64, title string, blockType BlockType, expiresAt *time.Time, now time.Time) error {
	upd, err := s.sqlText(s.scriptName("update"))
	if err != nil {
		return err
	}
	nowUTC := now.UTC()
	title = truncateTitle(title, 0)
	if s.isBlack {
		_, err = s.edb.Exec(upd, title, int(blockType), utcOrNil(expiresAt), nowUTC, id)
	} else {
		_, err = s.edb.Exec(upd, title, nowUTC, id)
	}
	if err != nil {
		return fmt.Errorf("shield: 更新 %s 失败: %w", s.table, err)
	}
	return nil
}

// SoftDelete 软删除条目（deleted_at = now，可恢复）。
func (s *IPListStore) SoftDelete(id int64, now time.Time) error {
	del, err := s.sqlText(s.scriptName("soft_delete"))
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(del, now.UTC(), now.UTC(), id); err != nil {
		return fmt.Errorf("shield: 软删 %s 失败: %w", s.table, err)
	}
	return nil
}

// Restore 恢复软删条目（清除 deleted_at）。
func (s *IPListStore) Restore(id int64, now time.Time) error {
	rst, err := s.sqlText(s.scriptName("restore"))
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(rst, now.UTC(), id); err != nil {
		return fmt.Errorf("shield: 恢复 %s 失败: %w", s.table, err)
	}
	return nil
}

// Import 批量导入（每行一个精确 IP/CIDR）。幂等：ip 已存在（含软删/过期）跳过。
// 返回成功导入数与跳过数。
func (s *IPListStore) Import(ips []string, title string, blockType BlockType, now time.Time) (imported, skipped int, err error) {
	imp, err := s.sqlText(s.scriptName("import"))
	if err != nil {
		return 0, 0, err
	}
	nowUTC := now.UTC()
	title = truncateTitle(title, 0)
	for _, line := range ips {
		ip := strings.TrimSpace(line)
		if ip == "" || strings.HasPrefix(ip, "#") {
			continue
		}
		var res sqlResult
		if s.isBlack {
			res, err = s.edb.Exec(imp, ip, title, int(blockType), 0, nowUTC, nowUTC) // 导入初始 warn_times=0
		} else {
			res, err = s.edb.Exec(imp, ip, title, nowUTC, nowUTC)
		}
		if err != nil {
			return imported, skipped, fmt.Errorf("shield: 导入 %s 失败（ip=%s）: %w", s.table, ip, err)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			imported++
		} else {
			skipped++
		}
	}
	return imported, skipped, nil
}

// List 分页查询列表并返回总条数（管理面展示；行值已归一化：数值 → int64、时间 → RFC3339）。
func (s *IPListStore) List(f ListFilter, now time.Time) (rows []map[string]any, total int64, err error) {
	if f.Limit <= 0 {
		f.Limit = defaultListLimit
	}
	if f.Limit > maxListLimit {
		f.Limit = maxListLimit
	}
	validOnly := 0
	if f.ValidOnly {
		validOnly = 1
	}
	sel, err := s.sqlTextOrder(s.scriptName("query_list"), f.Sort) // {order} 经白名单映射注入
	if err != nil {
		return nil, 0, err
	}
	var args []any
	if s.isBlack {
		args = []any{f.IP, f.IP, int(f.BlockType), int(f.BlockType), validOnly, now.UTC(), f.Limit, f.Offset}
	} else {
		args = []any{f.IP, f.IP, validOnly, f.Limit, f.Offset}
	}
	if err := s.edb.GetMany(sel, &rows, args...); err != nil {
		return nil, 0, fmt.Errorf("shield: 查询 %s 列表失败: %w", s.table, err)
	}
	for _, row := range rows {
		normalizeListRow(row, s.isBlack)
	}
	cnt, err := s.sqlText(s.scriptName("count"))
	if err != nil {
		return nil, 0, err
	}
	var cntArgs []any
	if s.isBlack {
		cntArgs = []any{f.IP, f.IP, int(f.BlockType), int(f.BlockType), validOnly, now.UTC()}
	} else {
		cntArgs = []any{f.IP, f.IP, validOnly}
	}
	var totalRows []map[string]any
	if err := s.edb.GetMany(cnt, &totalRows, cntArgs...); err != nil {
		return nil, 0, fmt.Errorf("shield: 统计 %s 总数失败: %w", s.table, err)
	}
	if len(totalRows) > 0 {
		total = eventToInt64(totalRows[0]["total"])
	}
	return rows, total, nil
}

// AddHitCount 黑名单命中计数累加（后台攒批 flush 调用；白名单调用返回错误）。
func (s *IPListStore) AddHitCount(id int64, delta int) error {
	if !s.isBlack {
		return fmt.Errorf("shield: 白名单无 hit_count 列")
	}
	upd, err := s.sqlText(s.scriptName("hit_count"))
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(upd, delta, id); err != nil {
		return fmt.Errorf("shield: 累加 %s 命中计数失败: %w", s.table, err)
	}
	return nil
}

// BanEntry 黑名单条目全状态行（封禁三态判定/续封转永久判定用；含软删/过期）。
type BanEntry struct {
	ID        int64
	IP        string
	Title     string
	BlockType BlockType
	HitCount  int64
	WarnTimes int64
	ExpiresAt string // RFC3339；"" = 永久（NULL）
	Deleted   bool   // deleted_at 非空
}

// GetByIP 按精确 ip 取单条全状态行（软删/过期也返回，供封禁三态判定）。
// 无记录返回 ErrIPNotExists。
func (s *IPListStore) GetByIP(ip string) (*BanEntry, error) {
	if !s.isBlack {
		return nil, fmt.Errorf("shield: 白名单无封禁语义")
	}
	sel, err := s.sqlText(s.scriptName("get"))
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := s.edb.GetMany(sel, &rows, ip); err != nil {
		return nil, fmt.Errorf("shield: 查询 %s 条目失败（ip=%s）: %w", s.table, ip, err)
	}
	if len(rows) == 0 {
		return nil, ErrIPNotExists
	}
	row := rows[0]
	e := &BanEntry{
		ID:        eventToInt64(row["id"]),
		IP:        eventToString(row["ip"]),
		Title:     eventToString(row["title"]),
		BlockType: BlockType(eventToInt64(row["block_type"])),
		HitCount:  eventToInt64(row["hit_count"]),
		WarnTimes: eventToInt64(row["warn_times"]),
		ExpiresAt: "",
		Deleted:   row["deleted_at"] != nil, // NULL = 未软删（勿经 eventToString：nil 归一为 "<nil>" 非空串）
	}
	if row["expires_at"] != nil {
		e.ExpiresAt = eventToString(row["expires_at"])
	}
	return e, nil
}

// 封禁续封语义常量（IP_BLACKLIST_PLAN §3.4 决策 8/10）。
const (
	// banWarnTimesLimit 封禁次数上限：限时封禁续封累计达 5 次转永久。
	banWarnTimesLimit = 5
	// banPermanentTitleSuffix 转永久时追加到 title 的标记。
	banPermanentTitleSuffix = "（累计封禁达 5 次转永久）"
)

// ipTitleMaxRunes title 列宽上限（三方言建表脚本均为 VARCHAR(64)，按字符计）。
const ipTitleMaxRunes = 64

// truncateTitle 截断 title 至列宽内（utf-8 按字符计；reserve>0 为后续要追加的标记
// 如转永久后缀预留空间）。超长不截断会在 MySQL 严格模式下 INSERT/UPDATE 直接报错。
func truncateTitle(title string, reserve int) string {
	max := ipTitleMaxRunes - reserve
	if max < 1 {
		max = 1
	}
	rs := []rune(strings.TrimSpace(title))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max])
}

// RestoreBan 封禁恢复续封（软删/过期条目拉回小黑屋）：清 deleted_at、expires_at 按
// 调用方时长重设（人工=所选时长 / 自动=TTL×10）、warn_times 原子自增（SQL 侧
// warn_times = warn_times + 1，消除与并发写方的读-改-写竞态）。
// +1 后 warn_times ≥ 5 且本次为限时封禁（expiresAt 非 nil）→ 转永久：expires_at 置 NULL
// 且 title 追加转永久标记，返回 toPermanent=true 供端点提示（判定基于读取时刻的
// warn_times 快照，极端并发下可能延后一轮追加标记，不影响封禁正确性）。
// ip 无记录返回 ErrIPNotExists；白名单调用报错。
func (s *IPListStore) RestoreBan(ip string, expiresAt *time.Time, now time.Time) (toPermanent bool, err error) {
	if !s.isBlack {
		return false, fmt.Errorf("shield: 白名单无封禁语义")
	}
	cur, err := s.GetByIP(ip)
	if err != nil {
		return false, err
	}
	warn := cur.WarnTimes + 1
	exp := expiresAt
	title := cur.Title
	if cur.ExpiresAt == "" {
		// 本就永久的条目：恢复后仍永久（调用方时长仅适用于限时条目，避免把永久封禁降级）
		exp = nil
	}
	// perm=true 仅表示「本次恢复把限时条目转成了永久」（端点据此提示）；本就永久的条目不报。
	if warn >= banWarnTimesLimit && expiresAt != nil && cur.ExpiresAt != "" {
		toPermanent = true
		exp = nil
		if !strings.Contains(title, banPermanentTitleSuffix) {
			title = truncateTitle(title, len([]rune(banPermanentTitleSuffix))) + banPermanentTitleSuffix
		}
	}
	upd, err := s.sqlText(s.scriptName("restore_ban"))
	if err != nil {
		return false, err
	}
	if _, err := s.edb.Exec(upd, utcOrNil(exp), truncateTitle(title, 0), now.UTC(), cur.ID); err != nil {
		return false, fmt.Errorf("shield: 封禁续封 %s 失败（ip=%s）: %w", s.table, ip, err)
	}
	return toPermanent, nil
}

// Jail 小黑屋查询（IP_BLACKLIST_PLAN §3.7）：当前在押的限时封禁条目——
// expires_at 非 NULL 且 > now、deleted_at 为 NULL；临近解封的在前（expires_at ASC）。
// 返回归一化行与在押总数（总数与 limit 无关，供前端提示"共 N 条在押"）。
func (s *IPListStore) Jail(now time.Time, limit int) (rows []map[string]any, total int64, err error) {
	if !s.isBlack {
		return nil, 0, fmt.Errorf("shield: 白名单无小黑屋语义")
	}
	if limit <= 0 {
		limit = defaultJailLimit
	}
	if limit > maxJailLimit {
		limit = maxJailLimit
	}
	sel, err := s.sqlText(s.scriptName("query_jail"))
	if err != nil {
		return nil, 0, err
	}
	rows = []map[string]any{} // 空态也回 []（非 null），前端表格无需判空
	if err := s.edb.GetMany(sel, &rows, now.UTC(), limit); err != nil {
		return nil, 0, fmt.Errorf("shield: 查询 %s 小黑屋失败: %w", s.table, err)
	}
	for _, row := range rows {
		normalizeListRow(row, true)
	}
	cnt, err := s.sqlText(s.scriptName("jail_count"))
	if err != nil {
		return nil, 0, err
	}
	var cntRows []map[string]any
	if err := s.edb.GetMany(cnt, &cntRows, now.UTC()); err != nil {
		return nil, 0, fmt.Errorf("shield: 统计 %s 小黑屋失败: %w", s.table, err)
	}
	if len(cntRows) > 0 {
		total = eventToInt64(cntRows[0]["total"])
	}
	return rows, total, nil
}

// 小黑屋分页默认与上限（首页轻量预览，见 IP_BLACKLIST_PLAN §3.7）。
const (
	defaultJailLimit = 20
	maxJailLimit     = 100
)

// 列表分页默认与上限（与 shield_event 查询约定一致）。
const (
	defaultListLimit = 500
	maxListLimit     = 10000
)

// normalizeListRow 列表行归一化：数值列 → int64；时间列 nil → ""、否则 RFC3339；其余 → string。
// ★ NULL 列在驱动层可能整键缺失（部分扫描器跳过 NULL 列），先补空串默认键，
// 保证响应字段集合稳定（前端/调用方不必判键存在性）。
func normalizeListRow(row map[string]any, isBlack bool) {
	for _, k := range []string{"expires_at", "deleted_at", "created_at", "updated_at"} {
		if _, ok := row[k]; !ok {
			row[k] = ""
		}
	}
	for k, v := range row {
		switch k {
		case "id", "block_type", "hit_count", "warn_times":
			row[k] = eventToInt64(v)
		case "expires_at", "deleted_at", "created_at", "updated_at":
			if v == nil {
				row[k] = ""
			} else {
				row[k] = eventToString(v)
			}
		default:
			if v != nil {
				row[k] = eventToString(v)
			}
		}
	}
}

// utcOrNil 时间指针 → UTC 时间或 nil（可空列传 nil 存 NULL）。
func utcOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// isUniqueErr 判定唯一约束冲突（三方言错误文案差异）。
func isUniqueErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "duplicate key value")
}

// sqlResult 收敛 easydb Exec 返回值类型（sql.Result 接口）。
type sqlResult = interface {
	LastInsertId() (int64, error)
	RowsAffected() (int64, error)
}
