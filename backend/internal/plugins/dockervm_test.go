package plugins

import (
	"runtime"
	"strings"
	"testing"
)

// TestRecommendVMSpecsFollowsHost 推荐规格必须跟着真机走，而不是写死。
// 这是用户的核心诉求：「应该自动跟随真机核心和内存」。
func TestRecommendVMSpecsFollowsHost(t *testing.T) {
	cases := []struct {
		name      string
		hostCores int
		hostMemMB int
		wantCores int
		wantMemMB int
	}{
		// 用户的机器：12 核 32G → 8 核 16G（留 1/3 CPU 给系统）
		{"12核32G", 12, 32768, 8, 16384},
		// sub2api 的服务器规格
		{"8核8G", 8, 8192, 5, 4096},
		// 小机器：不能低于下限
		{"2核4G", 2, 4096, 2, 4096},
		{"4核8G", 4, 8192, 2, 4096},
		// 大机器：封顶，不能把整机吃光
		{"32核128G", 32, 131072, 12, 24576},
		{"64核256G", 64, 262144, 12, 24576},
		// 探测失败（0）时给下限而不是 0
		{"未知规格", 0, 0, 2, 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cores, memMB := recommendVMSpecs(tc.hostCores, tc.hostMemMB)
			if cores != tc.wantCores {
				t.Errorf("cores = %d, want %d", cores, tc.wantCores)
			}
			if memMB != tc.wantMemMB {
				t.Errorf("memMB = %d, want %d", memMB, tc.wantMemMB)
			}
		})
	}
}

// TestRecommendVMSpecsNeverExceedsHost 推荐值不能超过真机拥有的资源，
// 否则 colima start 会直接失败。
func TestRecommendVMSpecsNeverExceedsHost(t *testing.T) {
	for _, hc := range []int{1, 2, 3, 4, 6, 8, 12, 16} {
		for _, hm := range []int{2048, 4096, 8192, 16384, 32768} {
			cores, memMB := recommendVMSpecs(hc, hm)
			if cores > hc {
				t.Errorf("宿主 %d 核，推荐了 %d 核", hc, cores)
			}
			if memMB > hm {
				t.Errorf("宿主 %dMB，推荐了 %dMB", hm, memMB)
			}
		}
	}
}

// TestRecommendVMSpecsAlignedToGB 内存要对齐到整 GB，
// colima --memory 只接受 GB 整数，7961MB 这种值会被截断成 7G。
func TestRecommendVMSpecsAlignedToGB(t *testing.T) {
	for _, hm := range []int{7961, 16000, 32768, 65536} {
		_, memMB := recommendVMSpecs(8, hm)
		if memMB%1024 != 0 {
			t.Errorf("宿主 %dMB → 推荐 %dMB，未对齐到整 GB", hm, memMB)
		}
	}
}

// TestRecommendedSpecsLiftThroughput 推荐规格必须真的能提升打码槽位。
// 把用户实测的两个点钉住：2 核 → 3 槽（约 6/分钟），推荐后应达到 sub2api 量级。
func TestRecommendedSpecsLiftThroughput(t *testing.T) {
	starved := capacityFromSpecs(2, 4096)
	recCores, recMemMB := recommendVMSpecs(12, 32768)
	recommended := capacityFromSpecs(recCores, recMemMB)
	if recommended <= starved {
		t.Fatalf("推荐规格槽位 %d 未超过欠配的 %d", recommended, starved)
	}
	// sub2api 8 核机器是 12 槽 / 30+ 每分钟，推荐值应达到同一量级
	if recommended < 12 {
		t.Errorf("12 核 32G 的推荐规格只有 %d 槽，低于 sub2api 的 12 槽基线", recommended)
	}
}

// TestDockerRuntimeReportsHostAndVM 快照必须同时给出宿主与 VM 规格，
// 否则前端无法解释「为什么 12 核的机器只有 3 个槽」。
func TestDockerRuntimeReportsHostAndVM(t *testing.T) {
	c := NewCenter(t.TempDir())
	info := c.DockerRuntime()
	if info.HostCores < 1 {
		t.Errorf("宿主核数应 >=1，实际 %d", info.HostCores)
	}
	if info.RecCores < 1 || info.RecMemMB < 1024 {
		t.Errorf("推荐规格无效: %d 核 / %dMB", info.RecCores, info.RecMemMB)
	}
	if info.RecSlots < 1 {
		t.Errorf("推荐槽位应 >=1，实际 %d", info.RecSlots)
	}
	// Linux 原生：没有 VM 层，必须标记为不可调并说明原因
	if runtime.GOOS == "linux" && info.Backend == BackendNativeLinux {
		if info.Resizable {
			t.Error("Linux 原生 Docker 不应标记为可调整规格")
		}
		if info.Message == "" {
			t.Error("Linux 原生应给出说明")
		}
	}
}

// TestApplyDockerVMSpecsRejectsOverHost 不能申请超过宿主的规格。
func TestApplyDockerVMSpecsRejectsOverHost(t *testing.T) {
	c := NewCenter(t.TempDir())
	info := c.DockerRuntime()
	if !info.Resizable {
		t.Skip("本机 Docker 运行方式不可调整规格，跳过")
	}
	if err := c.ApplyDockerVMSpecs(info.HostCores+8, 0, nil); err == nil {
		t.Error("核数超过宿主时应报错")
	}
	if err := c.ApplyDockerVMSpecs(0, info.HostMemMB+8192, nil); err == nil {
		t.Error("内存超过宿主时应报错")
	}
	if err := c.ApplyDockerVMSpecs(1, 512, nil); err == nil {
		t.Error("规格过低时应报错")
	}
}

// TestRemoveOnlyManagedContainers 删除必须只作用于 UmbraForge 自己创建的容器。
// 用户机器上还跑着 postgres/redis 等生产数据容器，误删不可逆。
func TestRemoveOnlyManagedContainers(t *testing.T) {
	c := NewCenter(t.TempDir())
	foreign := []string{"red-postgres", "red-redis", "my-app", "mysql"}
	for _, name := range foreign {
		if isManagedContainer(name) {
			t.Errorf("%q 被误判为 UmbraForge 托管容器", name)
		}
		err := c.ContainerAction(name, ActionRemove)
		if err == nil {
			t.Errorf("应拒绝删除非托管容器 %q", name)
			continue
		}
		if !strings.Contains(err.Error(), "拒绝删除") {
			t.Errorf("%q 的拒绝原因应说明是非托管容器，实际: %v", name, err)
		}
	}
	for _, name := range []string{"umbraforge-auralith", "umbra-warp-41000"} {
		if !isManagedContainer(name) {
			t.Errorf("%q 应被识别为 UmbraForge 托管容器", name)
		}
	}
}

// TestPluginOfContainer 容器名要能反查回插件，停/删容器后插件状态才能回落。
func TestPluginOfContainer(t *testing.T) {
	cases := map[string]string{
		"umbraforge-auralith":   "auralith",
		"umbraforge-ezsolver":   "ezsolver",
		"umbraforge-veloraturn": "veloraturn",
		"/umbraforge-auralith":  "auralith",
		"red-postgres":          "",
		"umbra-warp-41000":      "",
		// 一体容器承载三个引擎，必须有自己的标记：否则它会掉进
		// 「其它项目」分支，用户在 Docker 页看不出这是打码容器。
		"umbraforge-captcha":  "captcha-all",
		"/umbraforge-captcha": "captcha-all",
	}
	for name, want := range cases {
		if got := pluginOfContainer(name); got != want {
			t.Errorf("pluginOfContainer(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestRemoveImageGuardsPublic 公共镜像默认不许删（可能是别的项目在用），
// 只有显式允许时才放行。
func TestRemoveImageGuardsPublic(t *testing.T) {
	c := NewCenter(t.TempDir())
	if err := c.RemoveImage("postgres:16-alpine", false); err == nil {
		t.Error("默认不应允许删除公共镜像")
	} else if !strings.Contains(err.Error(), "拒绝删除") {
		t.Errorf("拒绝原因不清晰: %v", err)
	}
	if err := c.RemoveImage("", false); err == nil {
		t.Error("空镜像名应报错")
	}
}

// TestContainerActionRejectsUnknown 只允许白名单动作，防止拼接出别的 docker 子命令。
func TestContainerActionRejectsUnknown(t *testing.T) {
	c := NewCenter(t.TempDir())
	for _, bad := range []string{"exec", "kill; rm -rf /", "", "RUN"} {
		if err := c.ContainerActionByName("umbraforge-auralith", bad); err == nil {
			t.Errorf("动作 %q 应被拒绝", bad)
		}
	}
}
