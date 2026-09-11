// traffic_range_test.go：流量统计时间范围取整单测。
// 关键不变式：缓存友好取整不得把区间压成零宽——跨度不足一个粒度（如「今日」在 00:00–00:59 内、
// 同小时自定义范围）时两端会截到同一整点，此时必须保持原区间，否则查询命中不到任何行。
package obs

import (
	"testing"
	"time"
)

func TestRoundTrafficRange(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name             string
		from, to         time.Time
		wantFrom, wantTo time.Time
	}{
		{
			"同小时内且起点对齐：保持原区间不压成零宽",
			time.Date(2026, 9, 11, 0, 0, 0, 0, utc),
			time.Date(2026, 9, 11, 0, 45, 0, 0, utc),
			time.Date(2026, 9, 11, 0, 0, 0, 0, utc),
			time.Date(2026, 9, 11, 0, 45, 0, 0, utc),
		},
		{
			"同小时内两端不同分钟：保持原区间",
			time.Date(2026, 9, 11, 3, 10, 0, 0, utc),
			time.Date(2026, 9, 11, 3, 50, 0, 0, utc),
			time.Date(2026, 9, 11, 3, 10, 0, 0, utc),
			time.Date(2026, 9, 11, 3, 50, 0, 0, utc),
		},
		{
			"跨度 24 小时：两端对齐整小时",
			time.Date(2026, 9, 11, 0, 20, 0, 0, utc),
			time.Date(2026, 9, 12, 0, 20, 0, 0, utc),
			time.Date(2026, 9, 11, 0, 0, 0, 0, utc),
			time.Date(2026, 9, 12, 0, 0, 0, 0, utc),
		},
		{
			"跨度恰好 1 小时：取整后仍非零宽",
			time.Date(2026, 9, 11, 5, 30, 0, 0, utc),
			time.Date(2026, 9, 11, 6, 30, 0, 0, utc),
			time.Date(2026, 9, 11, 5, 0, 0, 0, utc),
			time.Date(2026, 9, 11, 6, 0, 0, 0, utc),
		},
		{
			"跨度超 48 小时：两端对齐整天",
			time.Date(2026, 9, 1, 13, 0, 0, 0, utc),
			time.Date(2026, 9, 11, 7, 0, 0, 0, utc),
			time.Date(2026, 9, 1, 0, 0, 0, 0, utc),
			time.Date(2026, 9, 11, 0, 0, 0, 0, utc),
		},
	}
	for _, c := range cases {
		gotFrom, gotTo := roundTrafficRange(c.from, c.to)
		if !gotFrom.Equal(c.wantFrom) || !gotTo.Equal(c.wantTo) {
			t.Errorf("%s：got [%s, %s]，期望 [%s, %s]",
				c.name, gotFrom, gotTo, c.wantFrom, c.wantTo)
		}
		if !gotTo.After(gotFrom) {
			t.Errorf("%s：取整后区间必须非零宽，实际 [%s, %s]", c.name, gotFrom, gotTo)
		}
	}
}
