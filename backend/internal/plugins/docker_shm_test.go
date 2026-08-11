package plugins

import (
	"strings"
	"testing"
)

// 回归：--shm-size 曾写死 1g。Chrome 多进程把渲染画布/IPC 缓冲放 /dev/shm，
// 1g 在并发打码下会被打满，Chrome 退回磁盘临时文件（--disable-dev-shm-usage
// 的兜底路径）→ 变慢 + 进程堆积。amd64 生产打码机（裸机 systemd，稳定 12 并发）
// 用的是宿主 /dev/shm=3.9G，所以容器至少要给到同量级。
func TestDockerShmSizeIsClampedToProductionRange(t *testing.T) {
	got := dockerShmSize()
	if got == "" {
		t.Fatal("shm-size 不能为空")
	}
	// 只能是 Ng 或 Nm 形式
	if !strings.HasSuffix(got, "g") && !strings.HasSuffix(got, "m") {
		t.Fatalf("shm-size=%q 单位非法（应为 g 或 m）", got)
	}
	mb := shmSizeToMB(t, got)
	if mb < 1024 {
		t.Fatalf("shm-size=%s (%dMB) 低于 1g 下限 —— 并发打码会把 shm 打满", got, mb)
	}
	if mb > 4096 {
		t.Fatalf("shm-size=%s (%dMB) 超过 4g 上限 —— shm 是 tmpfs，占同一份 VM 内存", got, mb)
	}
}

// docker info 拿不到内存时必须回落到 2g（不是旧的写死 1g，也不能是空串）。
func TestDockerShmSizeFallbackWhenVMMemoryUnknown(t *testing.T) {
	// dockerVMMemoryMB 依赖 docker CLI；这里只断言纯函数的夹取逻辑覆盖了
	// 「读不到」这一支：totalMB<=0 → "2g"。
	// 通过对 dockerShmSize 的输出做范围检查间接保证（见上一个测试），
	// 这里显式验证夹取边界。
	cases := []struct {
		totalMB int
		want    string
	}{
		{0, "2g"},       // 读不到
		{-1, "2g"},      // 异常值
		{1024, "1g"},    // 1G VM → half=512 → 夹到下限
		{2048, "1024m"}, // 2G VM → half=1024
		{5771, "2885m"}, // 实测 colima VM 配额
		{16384, "4g"},   // 16G VM → half=8192 → 夹到上限
	}
	for _, tc := range cases {
		got := clampShmSize(tc.totalMB)
		if got != tc.want {
			t.Errorf("clampShmSize(%d) = %q, want %q", tc.totalMB, got, tc.want)
		}
	}
}

// docker run 参数必须带上 cgroup 压力接口与采样间隔（与生产机对齐）。
// 缺了 MEMORY_PRESSURE_WATCH，auralith 就只能用逐进程 RSS 累加判资源，
// 把 Chrome 多进程共享的物理页重复计（实测 cgroup 2348MB vs 累加 7481MB），
// 一上并发立刻 503 resource high-water exceeded。
func TestDockerRunArgsCarryMemoryPressureAndShm(t *testing.T) {
	c := &Center{}
	args := c.dockerRunArgsCombined("test-image")
	joined := strings.Join(args, " ")

	for _, must := range []string{
		"MEMORY_PRESSURE_WATCH=/sys/fs/cgroup/memory.pressure",
		"AURALITH_RESOURCE_SAMPLE_MS=1000",
		"--shm-size=",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("docker run 参数缺少 %q\n实际: %s", must, joined)
		}
	}
	// 不能再出现写死的 1g（除非 VM 真的只有 1-2G，那时 clamp 会给 1g ——
	// 所以只断言「不是硬编码字面量」的方式：shm-size 必须由 dockerShmSize 产出）
	if strings.Contains(joined, "--shm-size=1g") && dockerShmSize() != "1g" {
		t.Error("shm-size 仍是硬编码 1g，未走自适应")
	}
}

func shmSizeToMB(t *testing.T, s string) int {
	t.Helper()
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	n := 0
	for _, r := range num {
		if r < '0' || r > '9' {
			t.Fatalf("shm-size=%q 数值部分非法", s)
		}
		n = n*10 + int(r-'0')
	}
	if unit == 'g' {
		return n * 1024
	}
	return n
}
