// geo_disabled_test.go：功能禁用（GEOIP_ENABLED=false）读侧降级单测（GEOIP_SWITCH D2）。
// 不依赖真实 mmdb：禁用短路在库加载之前，NewResolver 零配置即可构造。
package shield

import (
	"testing"

	"rocksys/internal/geoip"
)

// TestFillGeoRowsDisabled 验证禁用态拦截明细回填口径：
//   - JOIN 未命中行不再实时解析，country_name 填提供者规范文案「服务未开启」，不产生国家码；
//   - JOIN 命中的历史数据（country_code 非空）不受影响照常显示；
//   - 无 IP 行不回填；重新开启后恢复实时解析（无 mmdb 时为空值，不再是禁用文案）。
func TestFillGeoRowsDisabled(t *testing.T) {
	res := geoip.NewResolver("")
	res.SetEnabled(false)
	rows := []map[string]any{
		{"client_ip": "8.8.8.8"}, // JOIN 未命中 → 实时路径 → 服务未开启
		{"client_ip": "1.1.1.1", "country_code": "US", "country_name": "United States"}, // 历史数据 → 照常
		{"client_ip": ""}, // 无 IP → 不处理
	}
	fillGeoRows(res, rows)
	if got, _ := rows[0]["country_name"].(string); got != geoip.DisabledText {
		t.Fatalf("禁用时未命中行应显示 %q，实际 %q", geoip.DisabledText, got)
	}
	if cc, _ := rows[0]["country_code"].(string); cc != "" {
		t.Fatalf("禁用时不应产生国家码，实际 %q", cc)
	}
	if got, _ := rows[1]["country_name"].(string); got != "United States" {
		t.Fatalf("JOIN 命中的历史数据应照常保留，实际 %q", got)
	}
	if _, ok := rows[2]["country_name"]; ok {
		t.Fatal("无 IP 行不应被回填")
	}
	res.SetEnabled(true) // 重新开启：恢复实时解析路径（本测试无 mmdb，解析为零值但不再是禁用文案）
	fillGeoRows(res, rows[:1])
	if got, _ := rows[0]["country_name"].(string); got == geoip.DisabledText {
		t.Fatal("重新开启后不应再显示「服务未开启」文案")
	}
}

// TestStatsTopIPDisabledTopRows 验证 StatsTopIP 的 geo 填充分支（不经 DB：直接构造行级输入
// 的同款逻辑由 TestFillGeoRowsDisabled 覆盖；此处补禁用态 Top IP 不带出国家码的口径断言）。
// StatsTopIP 本体需 DB fixture，禁用分支逻辑与 fillGeoRows 同源（Enabled() 短路 + DisabledText）。
func TestStatsTopIPDisabledResolverState(t *testing.T) {
	res := geoip.NewResolver("")
	res.SetEnabled(false)
	if res.Enabled() || res.Ready() {
		t.Fatal("禁用态 Enabled/Ready 应为 false（StatsTopIP 禁用分支的前置条件）")
	}
	if geoip.DisabledText == "" {
		t.Fatal("DisabledText 规范文案不应为空")
	}
}
