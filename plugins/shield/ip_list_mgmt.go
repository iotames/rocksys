// 动态 IP 黑白名单管理面代理（WAF 方案 §5.4/§6.1）。
//
// handler 层只管参数解析与响应包装；本层内聚业务：store 读写 + 写库成功后
// **主动重建快照**（管理面变更立即生效，不等 TTL 兜底），保证拦截链路与库一致。
// store 未注入（DB 未配置）时返回 ErrIPListDisabled，管理面端点据此回 503。
package shield

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/iotames/easyserver/log"
)

// ErrIPListDisabled DB 黑白名单未启用（store 未注入）。
var ErrIPListDisabled = errors.New("IP 黑白名单未启用（DB 未配置）")

// ErrInvalidIP ip 参数非法（非精确 IP 亦非 CIDR）。
var ErrInvalidIP = errors.New("ip 应为精确 IP 或 CIDR")

// IPListEnabled 黑白名单管理能力是否可用（DB 就绪且 store 注入）。
func (s *Shield) IPListEnabled(isBlack bool) bool {
	if isBlack {
		return s.ipBlackDB != nil
	}
	return s.ipWhiteDB != nil
}

// store 取对应黑白名单 store（nil 未注入时返回 ErrIPListDisabled）。
func (s *Shield) ipStore(isBlack bool) (ipListStore, error) {
	if isBlack {
		if s.ipBlackDB == nil {
			return nil, ErrIPListDisabled
		}
		return s.ipBlackDB, nil
	}
	if s.ipWhiteDB == nil {
		return nil, ErrIPListDisabled
	}
	return s.ipWhiteDB, nil
}

// rebuildAfter 写库成功后主动重建快照（失败告警不阻断：数据已落库，下轮 TTL 兜底生效）。
func (s *Shield) rebuildAfter(op string) {
	if err := s.Rebuild(); err != nil {
		log.Warn("shield: 管理面变更后重建快照失败（TTL 兜底将自动生效）", "op", op, "err", err.Error())
	}
}

// ListIPList 管理面列表（分页/过滤；行值已归一化）。
func (s *Shield) ListIPList(isBlack bool, f ListFilter) ([]map[string]any, int64, error) {
	st, err := s.ipStore(isBlack)
	if err != nil {
		return nil, 0, err
	}
	return st.List(f, time.Now())
}

// validBlackBlockType 黑名单语境 block_type 校验：0-11 放宽口径
// （0=其他兜底来源、1-10 真实拦截类别、11=人工收录；见 block_type.go 语境分离注释）。
func validBlackBlockType(bt BlockType) bool {
	return bt >= BlockOther && bt <= BlockManual
}

// AddIPList 新增一条（写库成功后重建快照，立即生效）。ip 非法返回 ErrInvalidIP；
// ip 已存在返回 ErrIPExists。黑名单 block_type 合法域 0-11、缺省 11 人工收录；白名单忽略。
func (s *Shield) AddIPList(isBlack bool, ip, title string, bt BlockType, exp *time.Time) (int64, error) {
	if !validIPEntry(ip) {
		return 0, ErrInvalidIP
	}
	if isBlack && !validBlackBlockType(bt) {
		return 0, fmt.Errorf("shield: block_type 应为 0-11 的整数（缺省 11=人工收录）")
	}
	st, err := s.ipStore(isBlack)
	if err != nil {
		return 0, err
	}
	if !isBlack {
		bt, exp = BlockIPBlacklist, nil // 白名单忽略类别/过期
	}
	id, err := st.Insert(ip, title, bt, exp, time.Now())
	if err != nil {
		return 0, err
	}
	s.rebuildAfter("add")
	return id, nil
}

// UpdateIPList 更新条目（黑名单：title/block_type/expires_at；白名单：title）。
func (s *Shield) UpdateIPList(isBlack bool, id int64, title string, bt BlockType, exp *time.Time) error {
	st, err := s.ipStore(isBlack)
	if err != nil {
		return err
	}
	if !isBlack {
		bt, exp = BlockIPBlacklist, nil
	}
	if err := st.Update(id, title, bt, exp, time.Now()); err != nil {
		return err
	}
	s.rebuildAfter("update")
	return nil
}

// SoftDeleteIPList 软删除（deleted_at = now；可恢复）。
func (s *Shield) SoftDeleteIPList(isBlack bool, id int64) error {
	st, err := s.ipStore(isBlack)
	if err != nil {
		return err
	}
	if err := st.SoftDelete(id, time.Now()); err != nil {
		return err
	}
	s.rebuildAfter("soft_delete")
	return nil
}

// RestoreIPList 恢复软删条目。
func (s *Shield) RestoreIPList(isBlack bool, id int64) error {
	st, err := s.ipStore(isBlack)
	if err != nil {
		return err
	}
	if err := st.Restore(id, time.Now()); err != nil {
		return err
	}
	s.rebuildAfter("restore")
	return nil
}

// ImportIPList 批量导入（每行一个精确 IP/CIDR；注释/空行忽略；重复幂等跳过）。
// 返回成功导入数与跳过数。黑名单 block_type 合法域 0-11、缺省 11 人工收录；白名单忽略。
func (s *Shield) ImportIPList(isBlack bool, ips []string, title string, bt BlockType) (int, int, error) {
	if isBlack && !validBlackBlockType(bt) {
		return 0, 0, fmt.Errorf("shield: block_type 应为 0-11 的整数（缺省 11=人工收录）")
	}
	st, err := s.ipStore(isBlack)
	if err != nil {
		return 0, 0, err
	}
	if !isBlack {
		bt = BlockIPBlacklist
	}
	imported, skipped, err := st.Import(ips, title, bt, time.Now())
	if err != nil {
		return imported, skipped, err
	}
	if imported > 0 {
		s.rebuildAfter("import")
	}
	return imported, skipped, nil
}

// BanIP 封禁三态入库（决策 8/10，自动拉黑/人工封禁共用；写库成功后重建快照）：
//   - 无记录 → 封禁入库（warn_times=1 起算）；
//   - 活跃条目（未删未过期）→ 跳过（written=false）；
//   - 软删/过期条目 → 恢复续封（warn_times+1，expires_at 按调用方时长重设；
//     累计达 5 次且为限时 → 转永久，perm=true 供端点提示）。
//
// 返回 (是否写入, 是否转永久, error)。
func (s *Shield) BanIP(ip, title string, bt BlockType, exp *time.Time) (written, perm bool, err error) {
	st, err := s.ipStore(true) // 封禁仅黑名单语境
	if err != nil {
		return false, false, err
	}
	if !validIPEntry(ip) {
		return false, false, ErrInvalidIP
	}
	cur, err := st.GetByIP(ip)
	switch {
	case errors.Is(err, ErrIPNotExists):
		if _, err := st.BanInsert(ip, title, bt, exp, time.Now()); err != nil {
			return false, false, err
		}
		s.rebuildAfter("ban_insert")
		return true, false, nil
	case err != nil:
		return false, false, err
	case banEntryActive(cur, time.Now()):
		return false, false, nil // 活跃条目：已在封禁中，跳过
	default:
		perm, err := st.RestoreBan(ip, exp, time.Now())
		if err != nil {
			return false, false, err
		}
		s.rebuildAfter("ban_restore")
		return true, perm, nil
	}
}

// SyncBlacklistFile 从外挂规则文件 rules/ip_blacklist.txt 同步 IP 入库（IP_BLACKLIST_PLAN §3.3）。
// 读取复用 Shield 现有 ruleLoader 路径（外挂优先、内嵌兜底），解析复用 parseRuleLines
// （# 注释/空行忽略），逐行 validIPEntry 校验（非法行计入 skipped），有效行走
// ImportIPList(true, lines, "来自 ip_blacklist.txt 同步", BlockManual)（幂等跳过已存在）。
// 文件缺失/为空（无可同步内容）返回错误，端点据此回 400。
func (s *Shield) SyncBlacklistFile() (imported, skipped int, err error) {
	rl, err := newRuleLoader(s.hub)
	if err != nil {
		return 0, 0, err
	}
	lines, err := rl.loadLines(ruleFileIPBlacklist)
	if err != nil {
		return 0, 0, err
	}
	valid := make([]string, 0, len(lines))
	for _, ip := range lines {
		if validIPEntry(ip) {
			valid = append(valid, ip)
		} else {
			skipped++
		}
	}
	if len(valid) == 0 {
		return 0, skipped, errors.New("规则文件中无有效 IP 行（文件缺失或仅有注释/空行）")
	}
	imp, skip, err := s.ImportIPList(true, valid, "来自 ip_blacklist.txt 同步", BlockManual)
	return imp, skipped + skip, err
}

// Jail 小黑屋查询（IP_BLACKLIST_PLAN §3.7）：当前在押的限时封禁条目，
// 临近解封的在前。limit 由 store 层收敛（默认 20、上限 100）。
func (s *Shield) Jail(limit int) ([]map[string]any, int64, error) {
	st, err := s.ipStore(true)
	if err != nil {
		return nil, 0, err
	}
	return st.Jail(time.Now(), limit)
}

// banEntryActive 判断封禁条目当前是否生效中（未软删且未过期或永久）。
// expires_at 为存储层归一字符串（RFC3339；"" = 永久）；解析失败视为已过期（放行续封）。
func banEntryActive(e *BanEntry, now time.Time) bool {
	if e.Deleted {
		return false
	}
	if e.ExpiresAt == "" {
		return true
	}
	exp, err := time.Parse(time.RFC3339, e.ExpiresAt)
	return err == nil && exp.After(now)
}

// validIPEntry 校验管理面录入/导入的 ip 为精确 IP 或 CIDR。
func validIPEntry(ip string) bool {
	if ip == "" {
		return false
	}
	if _, _, err := net.ParseCIDR(ip); err == nil {
		return true
	}
	return net.ParseIP(ip) != nil
}

// ipListKindName 管理面日志/错误提示用。
func ipListKindName(isBlack bool) string {
	if isBlack {
		return "黑名单"
	}
	return "白名单"
}

// errIPListOp 包装操作错误文案（响应不泄露内部细节，日志留明细）。
func errIPListOp(isBlack bool, op string, err error) error {
	return fmt.Errorf("shield: %s%s失败: %w", ipListKindName(isBlack), op, err)
}
