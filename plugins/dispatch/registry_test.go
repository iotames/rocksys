// Registry 真 registry 单测（STEP4）：记录集口径 / 三态 / 饱和递减 / 迟到递减兜底。
package dispatch

import "testing"

// TestRegistryEnsureInitialStates Ensure 登记初始态：探活任务灰态起步、免探活显绿。
func TestRegistryEnsureInitialStates(t *testing.T) {
	reg := NewRegistry()
	reg.Ensure(1, HealthUnknown)
	if got := reg.Health(1); got != HealthUnknown {
		t.Fatalf("探活任务节点初始态应为灰，got %v", got)
	}
	reg.Ensure(2, HealthOK)
	if got := reg.Health(2); got != HealthOK {
		t.Fatalf("免探活登记节点应为健康显绿，got %v", got)
	}
}

// TestRegistryEnsureKeepOrder 跨热更保序：已登记节点再次 Ensure 不清零既有探活结论。
func TestRegistryEnsureKeepOrder(t *testing.T) {
	reg := NewRegistry()
	reg.Ensure(1, HealthUnknown)
	reg.SetHealth(1, HealthOK)
	reg.Ensure(1, HealthUnknown) // 重登（如差量重启后重新登记）不得覆盖既有结论
	if got := reg.Health(1); got != HealthOK {
		t.Fatalf("重登后既有探活结论被清零，got %v", got)
	}
}

// TestRegistryRemoveTurnsGray 差量移除清记录转灰。
func TestRegistryRemoveTurnsGray(t *testing.T) {
	reg := NewRegistry()
	reg.Ensure(1, HealthUnknown)
	reg.SetHealth(1, HealthBad)
	reg.Remove(1)
	if got := reg.Health(1); got != HealthUnknown {
		t.Fatalf("移除后应转灰（记录不存在返回 HealthUnknown），got %v", got)
	}
	if got := reg.Len(); got != 0 {
		t.Fatalf("移除后记录数应为 0，got %d", got)
	}
	if got := reg.Inflight(1); got != 0 {
		t.Fatalf("移除后在途计数应随记录消失返回 0，got %d", got)
	}
}

// TestRegistrySaturationDec 饱和递减不为负。
func TestRegistrySaturationDec(t *testing.T) {
	reg := NewRegistry()
	reg.IncInflight(1)
	reg.IncInflight(1)
	reg.DecInflight(1)
	reg.DecInflight(1)
	reg.DecInflight(1) // 第三次递减时已为 0：饱和不再下降
	if got := reg.Inflight(1); got != 0 {
		t.Fatalf("饱和递减后应为 0，got %d", got)
	}
}

// TestRegistryDecMissingNoOp 记录不存在时递减 no-op。
func TestRegistryDecMissingNoOp(t *testing.T) {
	reg := NewRegistry()
	reg.DecInflight(42) // 未登记节点：不得登记、不得 panic
	if got := reg.Health(42); got != HealthUnknown {
		t.Fatalf("递减缺失记录不得自动登记，got %v", got)
	}
	if got := reg.Len(); got != 0 {
		t.Fatalf("递减缺失记录不得新增条目，got %d", got)
	}
}

// TestRegistryLateDecAfterReregister 节点失引用清记录后重新登记，迟到递减不得打成负数。
func TestRegistryLateDecAfterReregister(t *testing.T) {
	reg := NewRegistry()
	reg.IncInflight(1) // 在途 1
	reg.Remove(1)      // 失引用清记录
	reg.Ensure(1, HealthUnknown)
	reg.IncInflight(1) // 重新登记后新请求 +1
	reg.DecInflight(1)
	reg.DecInflight(1) // 迟到递减（旧请求的 -1）：饱和兜底
	if got := reg.Inflight(1); got != 0 {
		t.Fatalf("迟到递减不得打成负数，got %d", got)
	}
}

// TestRegistryIncAutoRegister 未登记节点 IncInflight 自动登记（选中即真实计入）。
func TestRegistryIncAutoRegister(t *testing.T) {
	reg := NewRegistry()
	reg.IncInflight(9)
	if got := reg.Inflight(9); got != 1 {
		t.Fatalf("未登记节点 +1 后应为 1，got %d", got)
	}
	if got := reg.Health(9); got != HealthUnknown {
		t.Fatalf("自动登记默认灰态，got %v", got)
	}
}
