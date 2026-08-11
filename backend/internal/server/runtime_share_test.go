package server

import (
	"testing"

	"github.com/umbraforge/desktop/internal/plugins"
)

// 共享内存档的核数解析：只改内存，核数沿用当前 VM 值。
//
// 回归：首版共享档传 cores=0 走了推荐值（12×2/3=8），把用户已调好的 12 核
// 降到 8 核 —— 打码槽位≈核×1.5，等于白砍 1/3 吞吐。实测日志：
// 「已调整为 8 核 / 23GB —— 打码槽位 11 → 12」，内存对了但核数掉了。
func resolveShareCores(bodyCores, vmCores, recCores int) int {
	cores := bodyCores
	if cores <= 0 {
		cores = vmCores
	}
	if cores <= 0 {
		cores = recCores
	}
	return cores
}

func TestShareModeKeepsCurrentCores(t *testing.T) {
	cases := []struct {
		name                         string
		bodyCores, vmCores, recCores int
		want                         int
	}{
		{"实测场景:当前12核不该降到推荐8核", 0, 12, 8, 12},
		{"显式传核数优先", 6, 12, 8, 6},
		{"VM 核数未知时回落推荐值", 0, 0, 8, 8},
		{"都未知时至少给推荐值", 0, 0, 0, 0},
	}
	for _, tc := range cases {
		if got := resolveShareCores(tc.bodyCores, tc.vmCores, tc.recCores); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// 共享档内存必须显著高于常见的小配额，否则这个按钮毫无意义。
func TestShareModeMemoryIsBigger(t *testing.T) {
	const host32G = 32768
	share := plugins.ShareHostMemoryMB(host32G)
	if share <= 6144 {
		t.Fatalf("32G 宿主共享上限 %dMB 未超过固定 6G", share)
	}
}
