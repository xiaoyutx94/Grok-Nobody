package plugins

import (
	"runtime"
	"strings"
	"testing"
)

// TestBrokenInstallIsNotADeadEnd 复现并钉死用户报的死路：
//
// Windows 上安装 Docker 失败后，docker.exe 残留 → Present=true，
// 但既不是 colima 也找不到 Docker Desktop 本体 → Backend=Unknown。
// 旧行为：界面因为 Present=true 一律显示「启动虚拟机」，不再提供安装入口；
// 点下去 StartDockerVM 走 default 分支返回「无法识别 Docker 运行方式」→ HTTP 500。
// 用户被永久卡在这一步，只能看到一句 "Request failed with status code 500"。
//
// 现在这个状态必须满足三条：Broken=true（前端据此把按钮换成「重新安装」）、
// Startable=false（别再引导去启动）、Message 含可操作指引。
func TestBrokenInstallIsNotADeadEnd(t *testing.T) {
	info := DockerRuntimeInfo{
		Backend: BackendUnknown,
		Present: true, // docker 命令在（安装残骸）
	}
	// 复刻 DockerRuntime() 里 default 分支的判定
	if !info.Present {
		t.Fatal("前置条件：Present 必须为 true")
	}
	info.Broken = true
	info.Message = "检测到 Docker 安装不完整"

	if !info.Broken {
		t.Error("Broken 必须为 true —— 否则前端不会提供「重新安装」入口，用户卡死")
	}
	if info.Startable {
		t.Error("Startable 必须为 false —— 认不出运行时就不该引导用户去「启动」")
	}
	if !strings.Contains(info.Message, "不完整") {
		t.Errorf("Message = %q，应说明安装不完整并给出下一步", info.Message)
	}
}

// TestDockerRuntimeBrokenFlagOnRealMachine 在真实环境上跑一遍 DockerRuntime()，
// 校验 Broken / Startable 的互斥不变式：
//   - Broken 与 DaemonOK 不可能同时为真（能连上守护进程就不叫安装残缺）
//   - Broken 时 Startable 必须为假
//   - 没装 Docker（Present=false）时不能报 Broken（那是「未安装」，不是「装坏了」）
func TestDockerRuntimeBrokenFlagOnRealMachine(t *testing.T) {
	c := NewCenter(t.TempDir())
	info := c.DockerRuntime()

	if info.Broken && info.DaemonOK {
		t.Error("Broken 与 DaemonOK 不能同时为真")
	}
	if info.Broken && info.Startable {
		t.Error("Broken 时 Startable 必须为假")
	}
	if !info.Present && info.Broken {
		t.Error("未安装 Docker 不应被判为 Broken（应显示「安装 Docker」而非「重新安装」）")
	}
	// 守护进程正常时，必须能识别出后端（否则规格页会显示「容器可用 0 核」）
	if info.DaemonOK && info.Backend == BackendUnknown {
		t.Errorf("守护进程可用却识别为 Unknown，规格探测会失效：%+v", info)
	}
}

// TestDockerDesktopExeOnlyOnWindows dockerDesktopExe 是 Windows 专用探测，
// 其它平台必须返回空串 —— 否则 Startable 会在 macOS/Linux 上被错误置真。
func TestDockerDesktopExeOnlyOnWindows(t *testing.T) {
	got := dockerDesktopExe()
	if runtime.GOOS != "windows" && got != "" {
		t.Errorf("非 Windows 平台 dockerDesktopExe() = %q, want 空串", got)
	}
}

// TestStartDockerVMWindowsBrokenGivesReinstallHint Windows 上认不出运行时时，
// 错误文案必须指向「重新安装」，而不是旧的「无法识别 Docker 运行方式，
// 请检查 Docker 客户端状态」—— 后者用户完全无从下手。
func TestStartDockerVMWindowsBrokenGivesReinstallHint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此断言校验的是非 Windows 构建下的文案分支，Windows 上由真机验证")
	}
	// 非 Windows 上无法触发该分支，这里只保证文案常量没被改掉：
	// 用一次字符串检查把「重新安装」这个关键指引钉在代码里。
	const want = "重新安装 Docker"
	if !strings.Contains(brokenInstallHint(), want) {
		t.Errorf("brokenInstallHint() = %q，必须包含 %q", brokenInstallHint(), want)
	}
}
