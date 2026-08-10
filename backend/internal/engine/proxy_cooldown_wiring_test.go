package engine

import (
	"testing"

	"github.com/umbraforge/desktop/internal/pkg/grokregister"
)

// TestProxyCooldownNormalization 冷却时长的归一化有三条语义，任何一条错都会
// 让功能静默失效或无法关闭：
//
//	0   = 未设置（老配置文件没这个字段）→ 必须回落默认值，
//	      否则升级后冷却在用户不知情的情况下静默关闭
//	负数 = 用户显式关闭 → 归一成 0（batch.go 用 >0 判断启用）
//	正数 = 原样透传
func TestProxyCooldownNormalization(t *testing.T) {
	def := grokregister.DefaultBatchConfig()

	cases := []struct {
		name string
		in   int
		want int
	}{
		{"未设置回落默认", 0, def.ProxyCooldownMinutes},
		{"显式关闭归一成0", -1, 0},
		{"任意负数都是关闭", -99, 0},
		{"正数原样透传", 25, 25},
		{"1 分钟也合法", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeBatchConfig(&grokregister.BatchConfig{
				ProxyCooldownMinutes: tc.in,
			})
			if out.ProxyCooldownMinutes != tc.want {
				t.Errorf("ProxyCooldownMinutes: in=%d → %d, want %d",
					tc.in, out.ProxyCooldownMinutes, tc.want)
			}
		})
	}
}

// TestProxyCooldownSurvivesNilConfig 完全没有 batch_config 时也要拿到默认冷却，
// 否则「从没进过设置页的用户」等于没这个功能。
func TestProxyCooldownSurvivesNilConfig(t *testing.T) {
	def := grokregister.DefaultBatchConfig()
	out := normalizeBatchConfig(nil)
	if out.ProxyCooldownMinutes != def.ProxyCooldownMinutes {
		t.Errorf("nil config 的 ProxyCooldownMinutes = %d, want %d",
			out.ProxyCooldownMinutes, def.ProxyCooldownMinutes)
	}
	if out.MaxProxyFails != def.MaxProxyFails {
		t.Errorf("nil config 的 MaxProxyFails = %d, want %d", out.MaxProxyFails, def.MaxProxyFails)
	}
}

// TestMaxProxyFailsStillNormalized 回归保护：加冷却字段时不能碰坏原有的
// 「失败几次换代理」归一化（0 → 默认 3）。
func TestMaxProxyFailsStillNormalized(t *testing.T) {
	def := grokregister.DefaultBatchConfig()
	out := normalizeBatchConfig(&grokregister.BatchConfig{MaxProxyFails: 0})
	if out.MaxProxyFails != def.MaxProxyFails {
		t.Errorf("MaxProxyFails = %d, want %d", out.MaxProxyFails, def.MaxProxyFails)
	}
	out = normalizeBatchConfig(&grokregister.BatchConfig{MaxProxyFails: 5})
	if out.MaxProxyFails != 5 {
		t.Errorf("MaxProxyFails = %d, want 5（用户显式值必须透传）", out.MaxProxyFails)
	}
}
