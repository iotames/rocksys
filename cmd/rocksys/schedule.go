// schedule.go：定时任务只读登记器（schedule_list 表的登记与查询）。
//
// 定位（任务工厂，非任务实例）：schedule_list 是「任务模板 + 轮次结果」的登记面——
// 登记有哪些任务、关联开关与上一轮执行结果，不驱动任何任务——各任务仍由各自触发循环执行，
// 本表提供统一入口的可见性（有哪些任务、关联开关、上次执行状态）。需要实时/精确触发的
// 内部机制（workpool 重试/worker 检查等）不纳入统一登记，以登记行 remark 与本注释标注。
//
// 模块边界：登记与只读端点放装配层（cmd/rocksys），internal 不 import plugins；
// enabled 由端点响应行内附带（服务端读 config_key 对应 easyconf 当前值），表内无 enabled 列——
// 配置中心是启用状态的唯一真源，消除「谁说了算」。
//
// 状态回写：本期仅 geoip_sync 行有回写者（geoSyncAll 收口单点回调，手动/定时皆经它）；
// 其 last_run_at 即「上次同步时间」。老任务零侵入，last_run_at 空表示「未登记」（页面标注，防误读为从未执行）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"rocksys/internal/conf"
	"rocksys/internal/db"

	"github.com/iotames/easyserver/log"
)

// ScheduleRow 一条登记行（与 schedule_list 表列一一对应；id/运行态列由表侧维护）。
type ScheduleRow struct {
	Name      string // 任务唯一标识（与代码常量绑定）
	Title     string // 中文名（展示）
	Kind      string // configurable / system
	ConfigKey string // 关联 easyconf 开关名（系统级空）
	Plan      string // 计划描述（仅展示）
	Remark    string // 说明（含"不纳入原因/只读"）
}

// 登记行 name 常量（与表内 name 一一绑定；回写按 name 定位）。
const (
	schedObsLogPrune      = "obs_log_prune"
	schedShieldEventPrune = "shield_event_prune"
	schedShieldAutoBan    = "shield_auto_ban"
	schedGeoipSync        = "geoip_sync"
	schedHotswapWatch     = "hotswap_watch"
	schedConfWatch        = "conf_watch"
	schedRegistryScan     = "registry_scan"
	schedDispatchHealth   = "dispatch_health"
	schedMqPoll           = "mq_poll"
	schedShieldIPListTTL  = "shield_iplist_ttl"
	schedShieldEventFlush = "shield_event_flush"
)

// PathScheduleList 定时任务只读清单端点（GET；装配期经 RegisterPlugin 注入）。
const PathScheduleList = "/admin/schedule/list"

// schedule_list.last_status 取值（权威定义，与 sql/<方言>/schedule_list_create_table.sql 注释、
// docs/DATA_DICT.md §3.5 三处一一对应）。
//
// 域边界：schedule 是「任务工厂/任务定义」的登记面——只登记任务模板（谁、关联什么开关、
// 什么节奏）与「上一轮调度执行的结果」；任务实例的运行时状态（进行中/取消/进度/互斥）
// 属任务执行中心，实例的全部应用状态只在任务中心查询，本表不承载。故取值全部是
// 「轮次执行结果」域词汇，无实例域词汇：
//
//	success   上一轮执行成功（含分趟同步「到点收工、下次续接」——分趟是设计内节奏，非异常）
//	failed    上一轮执行失败（真错误：库不可用、SQL 出错等）
//	skipped   该轮未执行（预留：前置条件不满足等场景）
//	partial   上一轮未跑完（人工经任务中心取消执行实例，已完成部分保留，下轮从断点续接）
const (
	ScheduleStatusSuccess = "success"
	ScheduleStatusFailed  = "failed"
	ScheduleStatusSkipped = "skipped"
	ScheduleStatusPartial = "partial"
)

// ScheduleRegistry schedule_list 登记器：EnsureTable/Upsert/ResetSystem/List/UpdateRunStatus。
type ScheduleRegistry struct {
	d *db.DB
}

// NewScheduleRegistry 构造登记器（dataDB 就绪时创建；nil 依赖由调用方保证）。
func NewScheduleRegistry(d *db.DB) *ScheduleRegistry {
	return &ScheduleRegistry{d: d}
}

// EnsureTable 幂等建表（schedule_list_create_table.sql）。
func (r *ScheduleRegistry) EnsureTable() error {
	ddl, err := r.sqlText("schedule_list_create_table.sql")
	if err != nil {
		return err
	}
	if _, err := r.d.EasyDB().Exec(ddl); err != nil {
		return fmt.Errorf("schedule: 建 schedule_list 表失败: %w", err)
	}
	return nil
}

// sqlText 读脚本并替换 {table} 占位符。
func (r *ScheduleRegistry) sqlText(name string) (string, error) {
	txt, err := r.d.SQL(name)
	if err != nil {
		return "", fmt.Errorf("schedule: 读取 SQL 脚本 %s 失败: %w", name, err)
	}
	return strings.ReplaceAll(txt, "{table}", db.TableScheduleList), nil
}

// Upsert 登记一行（按 name 冲突仅刷新登记列，运行态 last_* 保持不变）。
func (r *ScheduleRegistry) Upsert(row ScheduleRow) error {
	ins, err := r.sqlText("schedule_list_upsert.sql")
	if err != nil {
		return err
	}
	if _, err := r.d.EasyDB().Exec(ins,
		row.Name, row.Title, row.Kind, row.ConfigKey, row.Plan, row.Remark, time.Now().UTC()); err != nil {
		return fmt.Errorf("schedule: 登记任务 %s 失败: %w", row.Name, err)
	}
	return nil
}

// ResetSystem 系统级行整行重置：被外部改动后重启按 name 恢复为系统值，含运行态列清零。
// upsert 语义：全新库上系统级行首次登记也走本入口。
func (r *ScheduleRegistry) ResetSystem(row ScheduleRow) error {
	upd, err := r.sqlText("schedule_list_reset.sql")
	if err != nil {
		return err
	}
	if _, err := r.d.EasyDB().Exec(upd,
		row.Name, row.Title, row.Kind, row.ConfigKey, row.Plan, row.Remark, time.Now().UTC()); err != nil {
		return fmt.Errorf("schedule: 重置系统任务 %s 失败: %w", row.Name, err)
	}
	return nil
}

// scheduleMessageMaxRunes last_message 列宽上限（VARCHAR(255)，MySQL/PG 按字符计）。
const scheduleMessageMaxRunes = 255

// truncateRunes 按「字符」截断（禁止按字节切：按字节截断会把汉字切成半个，写入非法
// UTF-8，前端显示乱码——实测踩坑于同步报告的中文摘要）。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// UpdateRunStatus 回写任务运行状态（任务结束时单条原子 UPDATE；name=geoip_sync）。
func (r *ScheduleRegistry) UpdateRunStatus(name, status, message string) error {
	upd, err := r.sqlText("schedule_list_update_status.sql")
	if err != nil {
		return err
	}
	msg := truncateRunes(message, scheduleMessageMaxRunes)
	if _, err := r.d.EasyDB().Exec(upd, time.Now().UTC(), status, msg, time.Now().UTC(), name); err != nil {
		return fmt.Errorf("schedule: 回写任务 %s 状态失败: %w", name, err)
	}
	return nil
}

// UpdateSkipStatus 登记「该轮未执行」（skipped）：只记状态与原因，不动 last_run_at——
// 其语义为最近执行时间（执行结束时刻），跳过不是执行。
func (r *ScheduleRegistry) UpdateSkipStatus(name, message string) error {
	upd, err := r.sqlText("schedule_list_skip.sql")
	if err != nil {
		return err
	}
	if _, err := r.d.EasyDB().Exec(upd, ScheduleStatusSkipped, truncateRunes(message, scheduleMessageMaxRunes), time.Now().UTC(), name); err != nil {
		return fmt.Errorf("schedule: 登记任务 %s 跳过失败: %w", name, err)
	}
	return nil
}

// List 返回全部登记行（运行态列归一 string/NULL；enabled 由 enabledFn 按 config_key 现值计算，
// 系统级/空 config_key 恒 true）。只读，不改表。
func (r *ScheduleRegistry) List(enabledFn func(configKey string) bool) ([]map[string]any, error) {
	sel, err := r.sqlText("schedule_list_list.sql")
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := r.d.EasyDB().GetMany(sel, &rows); err != nil {
		return nil, fmt.Errorf("schedule: 查询登记清单失败: %w", err)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		key, _ := row["config_key"].(string)
		out = append(out, map[string]any{
			"name":         row["name"],
			"title":        row["title"],
			"kind":         row["kind"],
			"config_key":   key,
			"plan":         row["plan"],
			"last_run_at":  row["last_run_at"], // NULL 透传（未登记任务，页面标注防误读）
			"last_status":  row["last_status"],
			"last_message": row["last_message"],
			"remark":       row["remark"],
			"enabled":      enabledFn(key),
		})
	}
	return out, nil
}

// scheduleRows 装配期登记清单：可配型 4 + 系统级 7。
// system=true 的行走 ResetSystem 整行重置；其余走 Upsert（保留运行态）。
func scheduleRows(geoipReady bool) []struct {
	row    ScheduleRow
	system bool
} {
	geoipRemark := "手动：数据库页/概览聚合卡按钮；自动：GEOIP_SYNC_INTERVAL（0=关闭，最小 10 分钟）"
	if !geoipReady {
		geoipRemark += "；mmdb 未加载，自动同步未启动（放置 GeoLite2 mmdb 并重启后生效）"
	}
	return []struct {
		row    ScheduleRow
		system bool
	}{
		{ScheduleRow{schedObsLogPrune, "访问日志自动清理", "configurable", "OBS_LOG_PRUNE_ENABLED", "every@24h（首延迟 1 分钟）",
			"清理保留期外 access_log 行"}, false},
		{ScheduleRow{schedShieldEventPrune, "拦截明细自动清理", "configurable", "SHIELD_EVENT_PRUNE_ENABLED", "every@24h（首延迟 1 分钟）",
			"清理保留期外 shield_event 行"}, false},
		{ScheduleRow{schedShieldAutoBan, "自动拉黑引擎", "configurable", "SHIELD_AUTO_BAN_ENABLED", "window/3（≥1 分钟，启动即执行）",
			"窗口内超阈值自动拉黑"}, false},
		{ScheduleRow{schedGeoipSync, "GeoIP 关联表同步", "configurable", "GEOIP_SYNC_INTERVAL", "every@N 分钟（可配，0=关闭）", geoipRemark}, false},
		{ScheduleRow{schedHotswapWatch, "外挂文件监控", "system", "", "every@3s（HOT_FILES_WATCH_INTERVAL）",
			"hotscripts 下 sql/rules/trusted_proxies 变更自动热更；系统级只读"}, true},
		{ScheduleRow{schedConfWatch, "配置轮询热更", "system", "", "轮询工作目录 .env",
			"配置文件变更热加载；系统级只读"}, true},
		{ScheduleRow{schedRegistryScan, "注册中心心跳扫描", "system", "", "周期 TTL 巡检",
			"过期实例摘除；系统级只读"}, true},
		{ScheduleRow{schedDispatchHealth, "分发健康检查", "system", "", "周期健康探测",
			"上游健康状态维护；系统级只读"}, true},
		{ScheduleRow{schedMqPoll, "消息投递轮询", "system", "", "every@MQ_POLL_INTERVAL",
			"outbox 待投递消息轮询（仅 MQ_ENABLED=true 时运行）；系统级只读"}, true},
		{ScheduleRow{schedShieldIPListTTL, "IP 黑白名单快照重建", "system", "", "every@60s TTL 兜底刷新",
			"仅 DB 就绪时运行；系统级只读"}, true},
		{ScheduleRow{schedShieldEventFlush, "拦截事件批量落库", "system", "", "every@5s 或攒满 200 行",
			"异步缓冲批量写库（SHIELD_EVENT_LOG_ENABLED=false 时不落库）；系统级只读"}, true},
	}
}

// RegisterScheduleRows 装配期登记全部任务行（幂等 upsert；系统级整行重置，D20）。
func RegisterScheduleRows(reg *ScheduleRegistry, geoipReady bool) {
	for _, it := range scheduleRows(geoipReady) {
		var err error
		if it.system {
			err = reg.ResetSystem(it.row)
		} else {
			err = reg.Upsert(it.row)
		}
		if err != nil {
			log.Warn("schedule: 任务登记失败（页面清单将缺行）", "name", it.row.Name, "err", err.Error())
		}
	}
}

// scheduleEnabledOf 计算 config_key 对应任务的启用状态（读 easyconf 当前值，配置中心唯一真源）：
// 空 config_key（系统级）恒 true；GEOIP_SYNC_INTERVAL 按 0=关闭语义判定；其余 *_ENABLED 按 "true"。
func scheduleEnabledOf(items []conf.ConfigItem, geoipReady bool) func(string) bool {
	return func(key string) bool {
		if key == "" {
			return true
		}
		v := configValue(items, key)
		if key == "GEOIP_SYNC_INTERVAL" {
			return geoipReady && normalizeGeoSyncInterval(atoiDefault(v, 60)) != 0
		}
		return v == "true"
	}
}

// atoiDefault 整数解析（非法回落 def）。
func atoiDefault(s string, def int) int {
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil || s == "" {
		return def
	}
	return n
}

// ScheduleList GET /admin/schedule/list：只读登记清单（定时任务页数据源）。
// 响应：{ok:true, tasks:[{name,title,kind,config_key,plan,last_run_at,last_status,last_message,remark,enabled}]}。
func ScheduleList(reg *ScheduleRegistry, cfgMgr conf.Manager, geoipReady bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tasks, err := reg.List(scheduleEnabledOf(cfgMgr.List(), geoipReady))
		if err != nil {
			log.Error("schedule: 清单查询失败", "err", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "tasks": tasks})
	}
}
