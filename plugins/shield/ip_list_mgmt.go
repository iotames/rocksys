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

// AddIPList 新增一条（写库成功后重建快照，立即生效）。ip 非法返回 ErrInvalidIP；
// ip 已存在返回 ErrIPExists。
func (s *Shield) AddIPList(isBlack bool, ip, title string, bt BlockType, exp *time.Time) (int64, error) {
	if !validIPEntry(ip) {
		return 0, ErrInvalidIP
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
// 返回成功导入数与跳过数。
func (s *Shield) ImportIPList(isBlack bool, ips []string, title string, bt BlockType) (int, int, error) {
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
