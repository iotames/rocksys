// schedule_test.go：定时任务登记单测（sqlite 临时库）。
// 覆盖：登记 upsert 幂等（运行态保持）、系统级整行重置（D20）、状态回写（D21）、enabled 计算。
package main

import (
	"strings"
	"testing"

	"rocksys/internal/conf"
	"rocksys/internal/db"
)

func mustScheduleRegistry(t *testing.T) (*ScheduleRegistry, *db.DB) {
	t.Helper()
	d := openTestDB(t)
	reg := NewScheduleRegistry(d)
	if err := reg.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	return reg, d
}

func TestScheduleUpsertKeepsRunState(t *testing.T) {
	resetGeoSyncState()
	reg, _ := mustScheduleRegistry(t)
	if err := reg.Upsert(ScheduleRow{schedGeoipSync, "GeoIP 关联表同步", "configurable", "GEOIP_SYNC_INTERVAL", "every@N 分钟", ""}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.UpdateRunStatus(schedGeoipSync, "success", "同步完成"); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	// 再次登记（模拟重启）：登记列刷新，运行态保持
	if err := reg.Upsert(ScheduleRow{schedGeoipSync, "GeoIP 关联表同步（新标题）", "configurable", "GEOIP_SYNC_INTERVAL", "every@N 分钟", "新备注"}); err != nil {
		t.Fatalf("二次 Upsert: %v", err)
	}
	rows, err := reg.List(func(string) bool { return true })
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应 1 行，got %d", len(rows))
	}
	r := rows[0]
	if r["title"] != "GeoIP 关联表同步（新标题）" {
		t.Errorf("登记列应刷新，title=%v", r["title"])
	}
	if r["last_status"] != "success" || r["last_run_at"] == nil {
		t.Errorf("运行态应保持（last_status=%v last_run_at=%v）", r["last_status"], r["last_run_at"])
	}
}

func TestScheduleResetSystemRow(t *testing.T) {
	resetGeoSyncState()
	reg, d := mustScheduleRegistry(t)
	row := ScheduleRow{schedHotswapWatch, "外挂文件监控", "system", "", "every@3s", "系统级只读"}
	if err := reg.ResetSystem(row); err != nil {
		t.Fatalf("ResetSystem: %v", err)
	}
	// 模拟外部篡改登记列 + 运行态列
	exec(t, d, `UPDATE schedule_list SET title='改过的', last_status='success', remark='乱写的' WHERE name='`+schedHotswapWatch+`'`)
	// 重启重置
	if err := reg.ResetSystem(row); err != nil {
		t.Fatalf("二次 ResetSystem: %v", err)
	}
	rows, _ := reg.List(func(string) bool { return true })
	r := rows[0]
	if r["title"] != "外挂文件监控" || r["remark"] != "系统级只读" {
		t.Errorf("系统级行应恢复系统值，got title=%v remark=%v", r["title"], r["remark"])
	}
	if r["last_status"] != "" || r["last_run_at"] != nil {
		t.Errorf("系统级运行态应清零，got status=%v run_at=%v", r["last_status"], r["last_run_at"])
	}
}

func TestScheduleEnabledOf(t *testing.T) {
	resetGeoSyncState()
	items := []conf.ConfigItem{
		{Key: "OBS_LOG_PRUNE_ENABLED", Current: "true"},
		{Key: "SHIELD_EVENT_PRUNE_ENABLED", Current: "false"},
		{Key: "SHIELD_AUTO_BAN_ENABLED", Current: "true"},
		{Key: "GEOIP_SYNC_INTERVAL", Current: "0"},
	}
	fn := scheduleEnabledOf(items, true)
	if !fn("") {
		t.Error("系统级（空 config_key）应恒 true")
	}
	if !fn("OBS_LOG_PRUNE_ENABLED") || fn("SHIELD_EVENT_PRUNE_ENABLED") {
		t.Error("*_ENABLED 应按当前值 true/false 判定")
	}
	if fn("GEOIP_SYNC_INTERVAL") {
		t.Error("间隔 0 应判定为关闭")
	}
	// 间隔合法且 mmdb 就绪 → 启用；mmdb 未就绪 → 不启用（生效前置）
	items[3].Current = "60"
	if !scheduleEnabledOf(items, true)("GEOIP_SYNC_INTERVAL") {
		t.Error("间隔 60 且 mmdb 就绪应判定启用")
	}
	if scheduleEnabledOf(items, false)("GEOIP_SYNC_INTERVAL") {
		t.Error("mmdb 未就绪应判定不启用（生效前置）")
	}
}

func TestScheduleRegisterRows(t *testing.T) {
	resetGeoSyncState()
	reg, _ := mustScheduleRegistry(t)
	RegisterScheduleRows(reg, false) // mmdb 未加载：geoip_sync remark 注明依赖未满足
	rows, err := reg.List(func(string) bool { return true })
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 11 { // 可配型 4 + 系统级 7
		t.Fatalf("应登记 11 行，got %d", len(rows))
	}
	for _, r := range rows {
		if r["name"] == schedGeoipSync {
			remark, _ := r["remark"].(string)
			if !strings.Contains(remark, "mmdb 未加载") {
				t.Errorf("mmdb 未加载时 geoip_sync remark 应注明依赖未满足，got %q", remark)
			}
		}
	}
	// 幂等：重复登记（mmdb 已就绪）不增行，登记列随最新状态刷新
	RegisterScheduleRows(reg, true)
	rows, _ = reg.List(func(string) bool { return true })
	if len(rows) != 11 {
		t.Fatalf("重复登记应幂等 11 行，got %d", len(rows))
	}
	for _, r := range rows {
		if r["name"] == schedGeoipSync {
			if remark, _ := r["remark"].(string); strings.Contains(remark, "mmdb 未加载") {
				t.Errorf("mmdb 就绪后 remark 不应再注明依赖未满足，got %q", remark)
			}
		}
	}
}
