package plugins

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/umbraforge/desktop/internal/procutil"
)

// ── Windows 上 Docker 的宿主前置条件 ──
//
// 用户实测：Docker Desktop 装好了，但一启动就弹
// "Virtualization support not detected — Docker Desktop failed to start
//  because virtualisation support wasn't detected."
//
// 这不是本程序的 bug，是宿主缺前置条件（BIOS 未开虚拟化 / 未装 WSL2）。
// 但我们的界面此前完全没体现：
//   - 顶部照样显示「容器可用 24 核 / 24GB」「打码槽位 36」——那是 saved specs
//     或宿主规格的回落值，容器根本不存在，等于给用户看假数字；
//   - 「启动 Docker」按钮照样可点，点了必然失败，等满 4 分钟才报超时；
//   - 「一键部署全部」看着是禁用的（daemon_ok=false），但用户不知道为什么。
//
// 所以要在探测阶段就把「缺什么」查出来，直接告诉用户去开 BIOS 虚拟化 /
// 装 WSL2，而不是让他反复点一个注定失败的按钮。

// virtStatus 宿主虚拟化前置条件的判定结果。
type virtStatus struct {
	OK bool
	// Reason 不 OK 时的可操作原因（直接展示给用户）。
	Reason string
}

// detectWindowsVirtualization 检查 Windows 上跑 Docker Desktop 的两个硬前置：
//
//  1. CPU 虚拟化在固件里启用（Intel VT-x / AMD-V）。关着的话 WSL2 与 Hyper-V
//     都起不来，Docker Desktop 就是弹用户截图里那个框。
//  2. WSL2 可用。Docker Desktop 默认走 WSL2 后端。
//
// 用 PowerShell 读 WMI 的三个字段（两个来源的固件虚拟化状态）：
//   - Win32_ComputerSystem.HypervisorPresent：True 表示已有 hypervisor 在跑
//     （Hyper-V/WSL2 已启用），此时固件字段往往报 False/空（已被 hypervisor
//     接管），所以两者是「或」关系，不能只看后者 —— 这是个常见误判。
//   - Win32_ComputerSystem.VirtualizationFirmwareEnabled：**很多机器上是 null**
//     （不报告该字段），单独用它会把正常机器误判成「BIOS 未开」。
//   - Win32_Processor.VirtualizationFirmwareEnabled：每颗 CPU 的真实固件状态，
//     更可靠。两个固件来源任一为 True 即认为固件层没问题。
//
// 探测失败（拿不到 WMI）时返回 OK=true：宁可放行让 Docker 自己报错，
// 也不要因为探测本身不可靠而拦住一台其实能用的机器。
func detectWindowsVirtualization() virtStatus {
	if runtime.GOOS != "windows" {
		return virtStatus{OK: true}
	}
	ps := `$cs = Get-CimInstance Win32_ComputerSystem; ` +
		`$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1; ` +
		`"{0}|{1}|{2}" -f $cs.HypervisorPresent, $cs.VirtualizationFirmwareEnabled, $cpu.VirtualizationFirmwareEnabled`
	out, err := procutil.Command("powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", ps).Output()
	if err != nil {
		return virtStatus{OK: true} // 探测不了就不拦
	}
	hyperV, firmware, firmwareKnown := parseVirtFields(string(out))
	// 固件明确关闭（两个来源都读出 False）→ BIOS 未开虚拟化
	if firmwareKnown && !firmware && !hyperV {
		return virtStatus{
			OK: false,
			Reason: "CPU 虚拟化未启用：Docker Desktop 无法启动（它会报 " +
				"Virtualization support not detected）。请重启进 BIOS/UEFI 打开 " +
				"Intel VT-x 或 AMD-V（常见位置：Advanced → CPU Configuration → " +
				"Intel Virtualization Technology / SVM Mode），保存后重启即可。" +
				"若这台机器本身是虚拟机，需在宿主上开启嵌套虚拟化。",
		}
	}
	// 固件没问题、但 hypervisor 没在跑：最常见原因是启用了
	// 「虚拟机平台」/Hyper-V 功能后**没重启系统**，hypervisor 尚未加载。
	// 此时 wsl --status 会报「当前计算机配置不支持 WSL2」，Docker Desktop
	// 的引擎（WSL2 VM）同样起不来。必须给出「重启系统」的可操作提示，
	// 否则用户会反复点「启动 Docker」白等超时（实测踩坑）。
	if !hyperV {
		if vmPlatformFeatureEnabled() {
			return virtStatus{
				OK: false,
				Reason: "Windows 的「虚拟机平台」功能已启用但尚未生效（hypervisor 未加载，通常是启用功能后没有重启系统）。" +
					"请保存工作并重启电脑，重启后 Docker Desktop 即可正常启动。",
			}
		}
		return virtStatus{
			OK: false,
			Reason: "「虚拟机平台」可选功能未启用，Docker Desktop 无法启动。" +
				"请以管理员身份运行：dism /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart，" +
				"然后重启电脑。",
		}
	}
	// hypervisor 在跑，再看 WSL2
	if !wsl2Available() {
		return virtStatus{
			OK: false,
			Reason: "未检测到可用的 WSL2：Docker Desktop 默认用它做后端。" +
				"请以管理员身份运行 `wsl --install`（或 `wsl --update`），完成后重启系统。",
		}
	}
	return virtStatus{OK: true}
}

// parseVirtFields 解析 "HypervisorPresent|CSFirmware|CPUFirmware" 输出。
// 抽成纯函数以便在非 Windows 上单测这个「或」关系判定。
// 固件字段可能是 null（空串）：两个来源任一为 True 即 firmware=true；
// 只有两个来源都明确读出 False 才认为 firmwareKnown=true（否则未知，
// 调用方不应据此判定「BIOS 未开」）。
func parseVirtFields(out string) (hypervisorPresent, firmwareEnabled, firmwareKnown bool) {
	parts := strings.SplitN(strings.TrimSpace(out), "|", 3)
	b := func(i int) (bool, bool) {
		if i >= len(parts) {
			return false, false
		}
		v := strings.TrimSpace(parts[i])
		if strings.EqualFold(v, "True") {
			return true, true
		}
		if strings.EqualFold(v, "False") {
			return false, true
		}
		return false, false
	}
	hypervisorPresent, _ = b(0)
	csF, csKnown := b(1)
	cpuF, cpuKnown := b(2)
	firmwareEnabled = csF || cpuF
	firmwareKnown = firmwareEnabled || (csKnown && cpuKnown)
	return
}

// wsl2Available 是否存在至少一个 WSL2 发行版所需的内核支持。
//
// 旧实现只看 `wsl --status` 输出非空 —— 但 WSL 出问题时输出照样非空
// （实测：功能启用但没重启时输出的是「当前计算机配置不支持 WSL2。
// 请启用"虚拟机平台"可选组件…」），于是把一台根本没有可用 WSL2 的
// 机器判成 OK，界面就错误地放行「启动 Docker」，点下去必然失败。
// 这里再检查语言无关的失败标记（健康输出里不会出现这两个 URL）：
//   - https://aka.ms/enablevirtualization —— VM 平台未生效（功能启用但未重启 / BIOS 未开）
//   - https://aka.ms/wsl2kernel         —— WSL2 内核缺失
func wsl2Available() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if _, err := exec.LookPath("wsl"); err != nil {
		return false
	}
	out, err := procutil.Command("wsl", "--status").CombinedOutput()
	if err != nil {
		return false
	}
	low := strings.ToLower(string(out))
	if strings.Contains(low, "enablevirtualization") || strings.Contains(low, "wsl2kernel") {
		return false
	}
	// wsl --status 的输出在不同语言版本里差异很大（且常是 UTF-16），
	// 所以不去匹配文案，只要命令成功返回、无失败标记且有内容就认为 WSL 可用。
	return len(strings.TrimSpace(string(out))) > 0
}

// vmPlatformFeatureEnabled 查询 Windows「虚拟机平台」（VirtualMachinePlatform）
// 可选功能是否已启用。用 Win32_OptionalFeature 只读查询，普通权限即可，
// 不依赖 DISM 提权。返回 false 表示查不到或未启用。
func vmPlatformFeatureEnabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	ps := `(Get-CimInstance -ClassName Win32_OptionalFeature -Filter "Name='VirtualMachinePlatform'").InstallState`
	out, err := procutil.Command("powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", ps).Output()
	if err != nil {
		return false // 探测不了就不拦（保持旧行为：放行让 Docker 自己报错）
	}
	return strings.TrimSpace(string(out)) == "1"
}

// uninstallDockerWindows 提权静默卸载 Docker Desktop。
//
// 界面此前没有卸载入口，而 Windows 上「装坏了」是常见状态（用户截图里就是
// 装好但起不来）。没有卸载按钮时用户只能去控制面板，且不知道要顺带清理什么。
func (c *Center) uninstallDockerWindows(t *PluginTask) error {
	wg := wingetPath()
	if wg == "" {
		return fmt.Errorf("未检测到 winget，无法自动卸载。请到「设置 → 应用」里手动卸载 Docker Desktop")
	}
	args := []string{
		"uninstall", "-e", "--id", "Docker.DockerDesktop",
		"--silent", "--disable-interactivity",
		"--accept-source-agreements",
	}
	c.taskStage(t.Key, "install", "正在卸载 Docker Desktop…")
	c.taskLog(t.Key, "$ winget "+strings.Join(args, " ")+"  （流式）")

	code, err := c.runWingetStreamed(t, wg, args)
	if err != nil {
		return err
	}
	// 卸载的成功码集合与安装不同：0x8A150014 = 没找到该包，对卸载而言等于「已经没了」
	switch uint32(code) {
	case 0, 0x8A150109, 0x8A150077:
		return nil
	case 0x8A150014:
		c.taskLog(t.Key, "winget 报告未安装 Docker Desktop（可能已被手动卸载），视为完成")
		return nil
	}
	return fmt.Errorf("%s", wingetExitHint(code))
}

// UninstallDockerAsync 异步卸载 Docker（Windows 走 winget 提权，
// macOS/Linux 复用既有 UninstallDocker）。前端轮询 docker-install 任务看进度。
func (c *Center) UninstallDockerAsync(removeAllContainers bool) PluginTask {
	c.mu.Lock()
	if t := c.tasks[taskKeyDockerInstall]; t != nil && t.Running {
		c.mu.Unlock()
		return *t
	}
	t := &PluginTask{
		Key:     taskKeyDockerInstall,
		Running: true,
		Stage:   "install",
		Message: "准备卸载 Docker…",
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Started: time.Now(),
		Updated: time.Now(),
	}
	c.tasks[taskKeyDockerInstall] = t
	c.mu.Unlock()

	go func() {
		defer func() { c.taskFinish(t.Key, t.OK, t.Stage, t.Message) }()

		// 顺序很重要：**先卸载，成功了再清容器**。
		//
		// 反过来做（先删容器再卸载）在卸载失败时是最坏结果：Docker 还在，
		// 但用户的打码容器被删了，等于白折腾一遍还得重新部署 3~8 分钟。
		// 我自己就这样误删过一次固化好的容器。
		//
		// 容器随 Docker 卸载一并消失，所以「卸载成功」路径根本不需要单独删；
		// 只有 removeAllContainers 语义（macOS/Linux 的 UninstallDocker 参数）
		// 需要它自己处理。
		if runtime.GOOS == "windows" {
			if err := c.uninstallDockerWindows(t); err != nil {
				t.Stage = "failed"
				t.Message = "卸载失败：" + err.Error() + "（Docker 与你的容器均未改动）"
				return
			}
		} else {
			if _, err := c.UninstallDocker(removeAllContainers); err != nil {
				t.Stage = "failed"
				t.Message = "卸载失败：" + err.Error() + "（Docker 与你的容器均未改动）"
				return
			}
		}
		c.invalidateDockerAvail()
		c.invalidateSpecCache()
		t.Stage = "ready"
		t.Message = "Docker 已卸载。需要时可点「安装 Docker」重新安装。"
		t.OK = true
		// 卸载成功后扫描残留的安装器下载缓存，交给前端弹窗询问是否删除
		// （三平台各自扫对应缓存路径，见 findDockerInstallerCaches）
		t.InstallerFiles = findDockerInstallerCaches()
	}()
	return *t
}
