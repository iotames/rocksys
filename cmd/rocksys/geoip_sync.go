// geoip_sync.go：GeoIP 历史数据回填（TRAFFIC_ANALYSIS 后续增量，2026-09-09 用户提出）。
//
// 背景：country/city 列为**写时解析**入库，上线前的历史行这两列为空串，统计时只能计「未知」。
// 本功能对 access_log / shield_event 两表中「有 IP 但 geo 缺失」的行做批量回填：
// DISTINCT 取缺失 IP → mmdb 逐 IP 解析 → 按 IP 分组批量 UPDATE（WHERE country=” 限定只补缺失行）。
// 私网/回环/解析不出的 IP 跳过并计数（永远无 geo，重复执行不空转）。
package main

import (
	"fmt"
	"strings"

	"rocksys/internal/db"
)

// geoLookup GeoIP 解析最小接口（*geoip.Resolver 天然满足；测试可注入桩）。
type geoLookup interface {
	Lookup(ip string) (country, city string)
	Ready() bool
}

// geoSyncReport 单表回填报告。
type geoSyncReport struct {
	Table       string   `json:"table"`
	IPs         int      `json:"ips"`          // 缺失 geo 的去重 IP 数（本趟处理）
	RowsUpdated int64    `json:"rows_updated"` // 回填行数
	Skipped     int      `json:"skipped"`      // 解析不出地理位置的 IP 数（私网/回环/库外地址）
	IPSample    []string `json:"ip_sample"`    // 跳过的 IP 样本（最多 5 个，供排查）
}

// geoipSyncTables 参与回填的表（表名固定，取值与 internal/db 权威常量一致）。
// geoSyncBatchIPs 单趟处理的缺失 IP 上限：防超大表首趟过久；未完可再次执行续填。
var (
	geoipSyncTables = []string{db.TableAccessLog, db.TableShieldEvent}
	geoSyncBatchIPs = 5000
)

// geoSyncTable 对单表执行一轮回填：DISTINCT 缺失 IP（单趟上限 geoSyncBatchIPs，可重复执行续填）
// → 解析 → 按 IP 批量 UPDATE。
func geoSyncTable(d *db.DB, table string, res geoLookup) (geoSyncReport, error) {
	rep := geoSyncReport{Table: table, IPSample: []string{}}
	ph := func(i int) string {
		if d.Driver() == "postgres" {
			return fmt.Sprintf("$%d", i)
		}
		return "?"
	}
	// DISTINCT 缺失 geo 的非空 IP（单趟限量，防超大表首趟过久；再点一次同步即续填）
	q := fmt.Sprintf(
		"SELECT DISTINCT client_ip FROM %s WHERE (country = '' OR country IS NULL) AND client_ip <> '' LIMIT %d",
		table, geoSyncBatchIPs)
	var ips []string
	if err := d.EasyDB().GetMany(q, &ips); err != nil {
		return rep, fmt.Errorf("geoip: 查询缺失 IP 失败: %w", err)
	}
	rep.IPs = len(ips)
	for _, ip := range ips {
		country, city := res.Lookup(ip)
		if country == "" && city == "" {
			rep.Skipped++
			if len(rep.IPSample) < 5 {
				rep.IPSample = append(rep.IPSample, ip)
			}
			continue
		}
		u := fmt.Sprintf(
			"UPDATE %s SET country = %s, city = %s WHERE client_ip = %s AND (country = '' OR country IS NULL)",
			table, ph(1), ph(2), ph(3))
		if res2, err := d.EasyDB().Exec(u, country, city, ip); err != nil {
			return rep, fmt.Errorf("geoip: 回填 %s 失败（ip=%s）: %w", table, ip, err)
		} else if n, err := res2.RowsAffected(); err == nil {
			rep.RowsUpdated += n
		}
	}
	return rep, nil
}

// geoSyncAll 对全部参与表执行回填。geo 未就绪直接报错（无 mmdb 时回填无从谈起）。
func geoSyncAll(d *db.DB, res geoLookup) ([]geoSyncReport, error) {
	if !res.Ready() {
		return nil, fmt.Errorf("geoip: mmdb 未加载，无法回填；请先放置 GeoLite2 mmdb 并重启服务")
	}
	var out []geoSyncReport
	for _, table := range geoipSyncTables {
		rep, err := geoSyncTable(d, table, res)
		if err != nil {
			return out, err
		}
		out = append(out, rep)
	}
	return out, nil
}

// geoSyncReportText 报告转人读文案（日志/前端展示共用）。
func geoSyncReportText(reps []geoSyncReport) string {
	var b strings.Builder
	for _, r := range reps {
		fmt.Fprintf(&b, "%s：回填 %d 行（处理 %d 个 IP，跳过 %d 个无地理信息 IP）；", r.Table, r.RowsUpdated, r.IPs, r.Skipped)
	}
	return strings.TrimSuffix(b.String(), "；")
}
