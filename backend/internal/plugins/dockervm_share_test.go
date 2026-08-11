package plugins

import "testing"

// 「与主机共享内存」= 宿主 3/4 作上限（vz 气球下是上限而非独占）。
// 回归背景：VM 配额被固定切到 6G，并发 solve 撞高水位全灭；而宿主 32G 有 70% 空闲。
func TestShareHostMemoryMB(t *testing.T) {
	cases := []struct {
		name   string
		hostMB int
		wantMB int
	}{
		{"32G 实测机型 → 24G", 32768, 24576},
		{"16G → 12G", 16384, 12288},
		{"8G → 6G", 8192, 6144},
		{"4G → 夹到 4G 下限", 4096, 4096},
		{"2G → 抬到 4G 下限但不超宿主", 2048, 2048},
		{"读不到宿主内存 → 回落上限常量", 0, recMemMaxMB},
		{"负值 → 回落上限常量", -1, recMemMaxMB},
	}
	for _, tc := range cases {
		if got := ShareHostMemoryMB(tc.hostMB); got != tc.wantMB {
			t.Errorf("%s: ShareHostMemoryMB(%d) = %d, want %d", tc.name, tc.hostMB, got, tc.wantMB)
		}
	}
}

// 共享档必须严格大于「固定 6G」这类小配额，否则这个按钮没有意义。
func TestShareHostMemoryBeatsSmallFixedQuota(t *testing.T) {
	const host32G = 32768
	share := ShareHostMemoryMB(host32G)
	if share <= 6144 {
		t.Fatalf("32G 宿主的共享上限 %dMB 未超过固定 6G —— 打码并发瓶颈仍在", share)
	}
	// 且必须留出宿主余量，不能把全部内存交给 VM（macOS 自身要用）。
	if share >= host32G {
		t.Fatalf("共享上限 %dMB 未给宿主留余量（宿主 %dMB）", share, host32G)
	}
}

// 结果必须对齐到整 GB：colima --memory 只接受整数 GB。
func TestShareHostMemoryIsGBAligned(t *testing.T) {
	for _, hostMB := range []int{32768, 31000, 16000, 7939, 12345} {
		if got := ShareHostMemoryMB(hostMB); got%1024 != 0 {
			t.Errorf("ShareHostMemoryMB(%d) = %d 未对齐 1GB（colima --memory 需整数 GB）", hostMB, got)
		}
	}
}
