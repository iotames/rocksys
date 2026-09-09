package geoip

import (
	"os"
	"testing"
)

// 真实 mmdb 集成测试：环境变量门控（GEOIP_INTEGRATION_DIR 指向含 GeoLite2 mmdb 的目录），
// 未设置即跳过。二进制 fixture 不入库，数据由本地自备（MaxMind GeoLite2 免费库）。
func TestRealMmdbIntegration(t *testing.T) {
	dir := os.Getenv("GEOIP_INTEGRATION_DIR")
	if dir == "" {
		t.Skip("未设置 GEOIP_INTEGRATION_DIR，跳过真实 mmdb 集成测试")
	}
	r := NewResolver(dir)
	gi := r.Lookup("8.8.8.8")
	if gi.Code == "" {
		t.Fatal("真实库查询 8.8.8.8 应至少返回国家码")
	}
	t.Logf("8.8.8.8 → code=%q country=%q city=%q ready=%v", gi.Code, gi.Country, gi.City, r.Ready())
}
