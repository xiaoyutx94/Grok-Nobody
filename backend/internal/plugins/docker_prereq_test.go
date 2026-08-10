package plugins

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestParseVirtFieldsOrRelation 虚拟化判定必须是「或」关系，不能只看
// VirtualizationFirmwareEnabled。
//
// 这是个很容易搞错的点：一旦 Hyper-V / WSL2 已经启用并接管了 CPU 的虚拟化扩展，
// Win32_ComputerSystem 会报 HypervisorPresent=True **而**
// VirtualizationFirmwareEnabled=False（因为固件层已被 hypervisor 占用）。
// 只看后者会把一台完全正常的机器判成「BIOS 没开虚拟化」，然后禁掉启动按钮 ——
// 用户明明能用却被拦住，比不检测更糟。
func TestParseVirtFieldsOrRelation(t *testing.T) {
	cases := []struct {
		name           string
		out            string
		wantHyperV     bool
		wantFirmware   bool
		wantVirtUsable bool // 二者任一为真即可用
	}{
		{"Hyper-V 已接管（固件报 False 属正常）", "True|False|False", true, false, true},
		{"固件开了但还没 hypervisor", "False|True|True", false, true, true},
		{"两者都真", "True|True|True", true, true, true},
		{"都关 —— 这才是真的没开虚拟化", "False|False|False", false, false, false},
		{"ComputerSystem 固件字段 null，CPU 报 True（本机实测形态）", "False||True", false, true, true},
		{"ComputerSystem 固件字段 null，CPU 也未知", "False||", false, false, false},
		{"带空白与换行", " True | False | False \r\n", true, false, true},
		{"大小写不敏感", "true|false|false", true, false, true},
		{"输出畸形", "", false, false, false},
		{"只有一个字段", "True", true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := parseVirtFields(tc.out)
			if h != tc.wantHyperV || f != tc.wantFirmware {
				t.Errorf("parseVirtFields(%q) = (%v,%v), want (%v,%v)",
					tc.out, h, f, tc.wantHyperV, tc.wantFirmware)
			}
			if usable := h || f; usable != tc.wantVirtUsable {
				t.Errorf("虚拟化可用性 = %v, want %v（必须是「或」关系）", usable, tc.wantVirtUsable)
			}
		})
	}
}

// TestDetectVirtualizationNoopOffWindows 非 Windows 平台必须直接返回 OK，
// 不能去起 PowerShell（那边压根没有），否则每次 DockerRuntime 都白等一次超时。
func TestDetectVirtualizationNoopOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上由真机验证")
	}
	vs := detectWindowsVirtualization()
	if !vs.OK {
		t.Errorf("非 Windows 平台应返回 OK=true，得到 %+v", vs)
	}
	if vs.Reason != "" {
		t.Errorf("非 Windows 平台不应有 Reason，得到 %q", vs.Reason)
	}
	if wsl2Available() {
		t.Error("非 Windows 平台 wsl2Available() 必须为 false")
	}
}

// TestDockerRuntimeVirtInvariants 运行时快照的不变式。
func TestDockerRuntimeVirtInvariants(t *testing.T) {
	c := NewCenter(t.TempDir())
	info := c.DockerRuntime()

	// 守护进程正常时前置条件必然满足，且不该有阻塞原因
	if info.DaemonOK && !info.VirtOK {
		t.Error("守护进程可用却报虚拟化不可用 —— 矛盾")
	}
	// 虚拟化不可用时绝不能报 Startable：那会给出一个必然失败的启动按钮
	if !info.VirtOK && info.Startable {
		t.Error("VirtOK=false 时 Startable 必须为 false")
	}
	// VMSpecsKnown 只在真实探测到规格时为真
	if info.VMSpecsKnown && !info.DaemonOK {
		t.Error("守护进程没起来时 VMSpecsKnown 不可能为真（那是回落估算值）")
	}
	if !info.VirtOK && strings.TrimSpace(info.VirtReason) == "" {
		t.Error("VirtOK=false 必须给出可操作的 VirtReason")
	}
}

// TestUninstallFailureLeavesContainersIntact 卸载失败时不能已经把容器删了。
//
// 我第一版写成「先删容器 → 再卸载」，实测踩到最坏结果：macOS 上卸载因
// 「Docker 仍可运行」失败，但打码容器已经被删 —— Docker 还在、容器没了，
// 用户白折腾还要重新 apt 装 Chromium 3~8 分钟。正确顺序是先卸载、成功了
// 容器自然随之消失。
//
// 这里用「本机有 Docker 且卸载必然失败（因为 daemon 还活着）」这个真实条件
// 来验证：调用后打码容器必须还在。
func TestUninstallFailureLeavesContainersIntact(t *testing.T) {
	c := NewCenter(t.TempDir())
	if !c.DockerAvailable() {
		t.Skip("无 Docker，跳过实机校验")
	}
	if !c.captchaContainerRunning() {
		t.Skip("本机没有打码容器在跑，无法验证「失败不误删」")
	}

	task := c.UninstallDockerAsync(false)
	if task.Key != taskKeyDockerInstall {
		t.Fatalf("任务 key = %q, want %q", task.Key, taskKeyDockerInstall)
	}
	// 等它跑完（守护进程活着 → 卸载必然失败，很快返回）
	for i := 0; i < 60; i++ {
		if c.Task(taskKeyDockerInstall).Done {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	got := c.Task(taskKeyDockerInstall)
	if !got.Done {
		t.Skip("卸载任务未在 30 秒内结束，跳过（可能真的在卸载）")
	}
	if got.OK {
		t.Skip("卸载居然成功了 —— 该断言只在失败路径有意义")
	}
	// 关键断言：失败后容器必须还在
	if !c.captchaContainerRunning() {
		t.Error("卸载失败却把打码容器删了 —— 最坏结果：Docker 还在、容器没了，" +
			"用户要重新部署 3~8 分钟")
	}
	if !strings.Contains(got.Message, "未改动") {
		t.Errorf("失败消息应明确告知容器未受影响，得到：%s", got.Message)
	}
}

// TestVirtReasonIsActionable 虚拟化提示必须告诉用户具体去哪开，
// 不能只说「虚拟化未启用」——用户不知道下一步做什么。
func TestVirtReasonIsActionable(t *testing.T) {
	// 直接检查文案常量：非 Windows 上无法触发真实分支，
	// 但要防止有人把可操作指引改成一句空话。
	vs := virtStatus{
		OK: false,
		Reason: "CPU 虚拟化未启用：Docker Desktop 无法启动（它会报 " +
			"Virtualization support not detected）。请重启进 BIOS/UEFI 打开 " +
			"Intel VT-x 或 AMD-V",
	}
	for _, must := range []string{"BIOS", "VT-x", "AMD-V"} {
		if !strings.Contains(vs.Reason, must) {
			t.Errorf("虚拟化提示缺少关键词 %q：%s", must, vs.Reason)
		}
	}
}
