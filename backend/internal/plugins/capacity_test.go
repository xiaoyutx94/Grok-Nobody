package plugins

import "testing"

// TestCapacityFromSpecsMatchesEngineAutotune 口径必须与三个打码引擎自身的
// auto-tune 一致（cores×1.5，按 400MB/并发 的内存封顶），否则我们注入的
// MAX_WORKERS 会与引擎的判断打架。
func TestCapacityFromSpecsMatchesEngineAutotune(t *testing.T) {
	cases := []struct {
		name    string
		cores   int
		memMB   int
		want    int
		comment string
	}{
		// sub2api 的实测机器：8 核 8G → 12 槽 → 30+/分钟
		{"sub2api 8核8G", 8, 8192, 12, "CPU 限"},
		// colima 默认：2 核 4G → 3 槽 → 约 6/分钟（用户遇到的就是这个）
		{"colima 默认 2核4G", 2, 3905, 3, "CPU 限"},
		// 调大后的 VM
		{"8核16G", 8, 16384, 12, "CPU 限"},
		{"10核24G", 10, 24576, 15, "CPU 限"},
		// 内存受限：核多但内存小
		{"8核2G 内存受限", 8, 2048, 4, "RAM 限"},
		{"4核1G 内存受限", 4, 1024, 2, "RAM 限"},
		// 兜底
		{"0核", 0, 4096, 1, "至少 1"},
		{"无内存信息只按CPU", 4, 0, 6, "CPU 限"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capacityFromSpecs(tc.cores, tc.memMB); got != tc.want {
				t.Errorf("capacityFromSpecs(%d, %d) = %d, want %d (%s)",
					tc.cores, tc.memMB, got, tc.want, tc.comment)
			}
		})
	}
}

// TestCapacityExplainsObservedThroughputGap 把用户报的现象钉成测试：
// 宿主 12 核 32G 但容器只有 2 核 4G 时，容量只有 3 —— 与观测到的
// 「约 6 个/分钟」吻合；而 sub2api 的 8 核机器容量 12 → 30+/分钟。
// 这证明差距来自容器算力配额，不是功能缺失。
func TestCapacityExplainsObservedThroughputGap(t *testing.T) {
	starved := capacityFromSpecs(2, 3905) // colima 默认
	sub2api := capacityFromSpecs(8, 8192) // 用户对比的服务器
	host := capacityFromSpecs(12, 32768)  // 宿主真实规格
	if starved >= sub2api {
		t.Fatalf("2 核容器容量 %d 不应 >= 8 核的 %d", starved, sub2api)
	}
	if host <= sub2api {
		t.Errorf("宿主 12 核容量 %d 应高于 8 核的 %d —— 说明算力本来是够的", host, sub2api)
	}
	if ratio := float64(sub2api) / float64(starved); ratio < 3 {
		t.Errorf("容量差只有 %.1fx，无法解释观测到的 5x 吞吐差", ratio)
	}
}

// TestClampToCapacityNeverOversubscribes 注入的槽数不能超过机器能力。
// 否则「上限」变成「强制」：2 核容器被塞进 12 个浏览器只会互相抢 CPU，
// 单次解题变慢、超时增多，吞吐反而更低。
func TestClampToCapacityNeverOversubscribes(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.setMode(PluginAuralith, ModeLocal) // 本机模式，走宿主规格，不 shell 出去
	capw := c.capacityWorkers(PluginAuralith)
	if capw < 1 {
		t.Fatalf("宿主容量算出 %d，应至少 1", capw)
	}
	if got := c.clampToCapacity(PluginAuralith, capw+50); got != capw {
		t.Errorf("请求 %d 应被压到容量 %d，实际 %d", capw+50, capw, got)
	}
	if got := c.clampToCapacity(PluginAuralith, 1); got != 1 {
		t.Errorf("低于容量的请求应原样保留，实际 %d", got)
	}
	// 0 表示交给引擎 auto-tune，不能被改成容量值
	if got := c.clampToCapacity(PluginAuralith, 0); got != 0 {
		t.Errorf("0（auto）应原样保留，实际 %d", got)
	}
}

// TestApplyWorkerEnvRespectsCapacity 端到端：注入值超过机器能力时，
// 真正写进环境变量的必须是被钳制后的值。
func TestApplyWorkerEnvRespectsCapacity(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.setMode(PluginEzSolver, ModeLocal)
	capw := c.capacityWorkers(PluginEzSolver)
	c.SetMaxWorkers(PluginEzSolver, capw+100)
	env := c.applyWorkerEnv(PluginEzSolver, nil)
	want := "MAX_WORKERS=" + itoa(capw)
	found := false
	for _, e := range env {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Errorf("期望注入 %s（机器容量），实际 %v", want, env)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
