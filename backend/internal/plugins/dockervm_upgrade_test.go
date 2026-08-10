package plugins

import "testing"

// TestRecIsUpgradeNeverPresentsDowngrade 回归：推荐值只取本机 2/3 的核，
// 所以用户手动给过更高配额时推荐值会**低于**当前。
// 界面上出现过「当前 18 槽 / 优化后可达 12 槽」这种把降配说成提升的展示，
// 这里把判定钉死：rec_is_upgrade 必须严格等于「推荐槽位 > 当前槽位」。
func TestRecIsUpgradeNeverPresentsDowngrade(t *testing.T) {
	cases := []struct {
		name        string
		curCores    int
		curMemMB    int
		hostCores   int
		hostMemMB   int
		wantUpgrade bool
		wantAbove   bool
	}{
		// 用户实际踩到的场景：VM 12 核（18 槽），推荐 8 核（12 槽）
		{"手动给到12核 高于推荐", 12, 15957, 12, 32768, false, true},
		// colima 默认 2 核（3 槽），推荐 8 核（12 槽）→ 确实是提升
		{"默认2核 低于推荐", 2, 3905, 12, 32768, true, false},
		// 正好等于推荐值：既不是提升也不算超配
		{"正好等于推荐", 8, 16384, 12, 32768, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recCores, recMemMB := recommendVMSpecs(tc.hostCores, tc.hostMemMB)
			cur := capacityFromSpecs(tc.curCores, tc.curMemMB)
			rec := capacityFromSpecs(recCores, recMemMB)

			gotUpgrade := rec > cur
			gotAbove := rec < cur
			if gotUpgrade != tc.wantUpgrade {
				t.Errorf("当前 %d 槽 / 推荐 %d 槽：rec_is_upgrade=%v，期望 %v",
					cur, rec, gotUpgrade, tc.wantUpgrade)
			}
			if gotAbove != tc.wantAbove {
				t.Errorf("当前 %d 槽 / 推荐 %d 槽：above_recommended=%v，期望 %v",
					cur, rec, gotAbove, tc.wantAbove)
			}
			// 核心不变式：标为提升时推荐必须真的更高
			if gotUpgrade && rec <= cur {
				t.Errorf("标为提升但推荐槽位 %d 未超过当前 %d", rec, cur)
			}
		})
	}
}

// TestDockerRuntimeUpgradeFlagsConsistent 两个标记不能同时为真。
func TestDockerRuntimeUpgradeFlagsConsistent(t *testing.T) {
	c := NewCenter(t.TempDir())
	info := c.DockerRuntime()
	if info.RecIsUpgrade && info.AboveRecommended {
		t.Error("rec_is_upgrade 与 above_recommended 不能同时为真")
	}
	if info.Undersized && !info.RecIsUpgrade {
		t.Error("判定为欠配时，推荐值必须是提升")
	}
	t.Logf("live: vm=%d核 cur=%d槽 rec=%d核 rec=%d槽 upgrade=%v above=%v",
		info.VMCores, info.CurSlots, info.RecCores, info.RecSlots,
		info.RecIsUpgrade, info.AboveRecommended)
}
