package shield

// block_type_test.go：block_type 枚举 0（其他）/11（人工收录）扩展的边界单测。
// 口径：shield_event 拦截事件永远只写 1-10；0/11 仅 ip_blacklist 表语境；
// 查询参数语境 0=全部（Valid() 保持 1-10 不变，见 block_type.go）。

import "testing"

// TestBlockTypeString 枚举中文名：0/11 特判、1-10 注册表、越界未知。
func TestBlockTypeString(t *testing.T) {
	cases := []struct {
		in   BlockType
		want string
	}{
		{BlockOther, "其他"},
		{BlockManual, "人工收录"},
		{BlockIPBlacklist, "IP黑名单"},
		{BlockPathRuleDeny, "路径规则"},
		{BlockType(12), "未知"},
		{BlockType(-1), "未知"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("String(%d) = %q, want %q", int(c.in), got, c.want)
		}
	}
}

// TestBlockTypeValid Valid() 保持 1-10 拦截语境口径（0=查询参数「全部」，不是合法存储枚举）。
func TestBlockTypeValid(t *testing.T) {
	for _, v := range []BlockType{1, 5, 10} {
		if !v.Valid() {
			t.Errorf("Valid(%d) 应为 true", int(v))
		}
	}
	for _, v := range []BlockType{0, 11, 12, -1} {
		if v.Valid() {
			t.Errorf("Valid(%d) 应为 false", int(v))
		}
	}
}
