package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/umbraforge/desktop/internal/procutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Docker 运行时（VM）的规格管理。
//
// 为什么需要这一层：Docker 在 macOS / Windows 上跑在一个 Linux VM 里，
// 容器拿到的算力是 **VM 的配额**，跟宿主机无关。colima 默认只给 2 核 2~4G，
// Docker Desktop 默认也远低于宿主规格。而打码是 CPU 密集的
// （一次 Turnstile ≈ 1 核 × 20 秒），VM 给多少核直接决定每分钟能注册多少个：
//
//	2 核  → 3 个并发槽  → 约 9/分钟
//	8 核  → 12 个并发槽 → 约 36/分钟
//
// 实测就踩过这个坑：宿主 12 核 32G，VM 只有 2 核 4G，吞吐只有别人 8 核机器的 1/5。
// 所以这里做三件事：探测当前 VM 规格 → 按宿主规格算推荐值 → 一键应用（重建 VM）。
// Linux 上容器直接跑在宿主内核上，没有 VM 层，不需要也不能调。

// DockerBackend 是 Docker 运行时的实现方式。
type DockerBackend string

const (
	BackendColima        DockerBackend = "colima"         // macOS/Linux 的 lima 虚拟机
	BackendDockerDesktop DockerBackend = "docker-desktop" // macOS/Windows 官方客户端
	BackendNativeLinux   DockerBackend = "native-linux"   // 宿主内核直跑，无 VM
	BackendUnknown       DockerBackend = "unknown"
)

// DockerRuntimeInfo 是 Docker 运行时的规格快照，前端据此展示与提示。
type DockerRuntimeInfo struct {
	Backend   DockerBackend `json:"backend"`
	Present   bool          `json:"present"`     // docker 可执行文件在不在
	DaemonOK  bool          `json:"daemon_ok"`   // 守护进程连得上
	Resizable bool          `json:"resizable"`   // 能否调整规格（Linux 原生不行）
	VMCores   int           `json:"vm_cores"`    // 容器实际可用核数
	VMMemMB   int           `json:"vm_mem_mb"`   // 容器实际可用内存
	HostCores int           `json:"host_cores"`  // 宿主核数
	HostMemMB int           `json:"host_mem_mb"` // 宿主内存

	// 推荐规格与它能带来的打码槽位
	RecCores   int  `json:"rec_cores"`
	RecMemMB   int  `json:"rec_mem_mb"`
	CurSlots   int  `json:"cur_slots"`
	RecSlots   int  `json:"rec_slots"`
	Undersized bool `json:"undersized"` // 当前明显低于推荐 → 前端提示一键优化
	// RecIsUpgrade 推荐值是否真的比当前更高。用户手动给过更高配额时推荐值会更低，
	// 此时不能把它当成「优化后可达」展示。
	RecIsUpgrade bool `json:"rec_is_upgrade"`
	// AboveRecommended 当前配额高于推荐值（用户主动加过配额）。
	AboveRecommended bool `json:"above_recommended"`

	// Startable 能否由本程序把 Docker 拉起来（Docker Desktop / colima 都算）。
	// 前端据此决定显示「启动 Docker」还是「重新安装」。
	Startable bool `json:"startable"`
	// VirtOK 宿主虚拟化前置条件是否满足（Windows：BIOS 虚拟化 + WSL2）。
	// false 时 Docker Desktop 根本起不来，会弹
	// "Virtualization support not detected" —— 此时「启动 Docker」按钮必须禁用，
	// 否则用户会反复点一个注定失败的按钮并等满 4 分钟超时。
	VirtOK bool `json:"virt_ok"`
	// VirtReason VirtOK=false 时的可操作原因（进 BIOS 开 VT-x / 装 WSL2）。
	VirtReason string `json:"virt_reason,omitempty"`
	// DesktopInstalled Docker Desktop 本体是否装着（Windows 用 exe 路径判定）。
	// 区分「没装」与「装了但起不来」——后者要给卸载/重装入口而不是安装入口。
	DesktopInstalled bool `json:"desktop_installed"`
	// VMSpecsKnown VMCores/VMMemMB 是否来自真实探测。
	//
	// 守护进程没起来时这两个值是 saved specs 或宿主规格的**回落值**，
	// 容器压根不存在。此前界面照样显示「容器可用 24 核 / 打码槽位 36」，
	// 而实际一个容器都没有 —— 给用户看假数字。前端据此显示「—」。
	VMSpecsKnown bool `json:"vm_specs_known"`
	// Broken 安装不完整：有 docker 命令但找不到运行时本体，通常是上次安装中断。
	//
	// 这个字段是用户实际踩到的坑：Windows 上安装失败后 docker.exe 残留，
	// Present 变 true，界面就永远显示「启动虚拟机」而不再提供安装入口，
	// 点下去必然失败（后端无法识别运行方式）—— 死路。有了 Broken，
	// 前端才能把按钮换成「重新安装 Docker」。
	Broken bool `json:"broken"`

	Message string `json:"message,omitempty"`
}

// 推荐规格的取值口径：
//   - CPU：宿主的 2/3（留 1/3 给 macOS/Windows 本身与浏览器等），至少 2，至多 12。
//     再高的收益被 Turnstile 挑战本身的网络等待摊薄，且会让桌面明显卡顿。
//   - 内存：宿主的 1/2，至少 4G，至多 24G。每个并发 Chromium ≈ 400MB，
//     12 核对应 12 槽 ≈ 4.8G，加上系统与页面缓存，8~16G 是舒适区。
const (
	recCPUNumerator   = 2
	recCPUDenominator = 3
	recCPUMin         = 2
	recCPUMax         = 12
	recMemRatio       = 2 // 宿主内存 / 2
	recMemMinMB       = 4096
	recMemMaxMB       = 24576
)

// recommendVMSpecs 按宿主规格算出建议给 Docker VM 的核数与内存。
func recommendVMSpecs(hostCores, hostMemMB int) (cores, memMB int) {
	cores = hostCores * recCPUNumerator / recCPUDenominator
	if cores < recCPUMin {
		cores = recCPUMin
	}
	if cores > recCPUMax {
		cores = recCPUMax
	}
	if hostCores > 0 && cores > hostCores {
		cores = hostCores
	}

	memMB = hostMemMB / recMemRatio
	// 对齐到 1G，避免出现 7961MB 这种数字
	memMB = (memMB / 1024) * 1024
	if memMB < recMemMinMB {
		memMB = recMemMinMB
	}
	if memMB > recMemMaxMB {
		memMB = recMemMaxMB
	}
	if hostMemMB > 0 && memMB > hostMemMB {
		memMB = (hostMemMB / 1024) * 1024
	}
	return cores, memMB
}

// detectDockerBackend 判断 Docker 是怎么跑的。
func (c *Center) detectDockerBackend() DockerBackend {
	if runtime.GOOS == "linux" {
		return BackendNativeLinux
	}
	// colima 上下文名就是 colima；也可能通过 DOCKER_HOST 指向 .colima
	if out, err := procutil.Command(dockerExecutable(), "context", "show").Output(); err == nil {
		switch strings.TrimSpace(string(out)) {
		case "colima":
			return BackendColima
		case "desktop-linux":
			return BackendDockerDesktop
		}
	}
	if _, err := exec.LookPath("colima"); err == nil {
		if out, err := procutil.Command("colima", "status").CombinedOutput(); err == nil &&
			strings.Contains(string(out), "running") {
			return BackendColima
		}
	}
	if dockerDesktopSettingsPath() != "" {
		return BackendDockerDesktop
	}
	// Windows 兜底：settings-store.json 只有在 Docker Desktop **至少启动过一次**
	// 之后才会生成。刚装完没开过（或安装中断）时，上面全部落空 → BackendUnknown，
	// 于是界面显示「未识别」、规格显示「容器可用 0 核」，点「启动虚拟机」直接 500。
	// 装了但没跑过是极常见的状态，必须按 Docker Desktop 识别。
	if runtime.GOOS == "windows" && dockerDesktopExe() != "" {
		return BackendDockerDesktop
	}
	return BackendUnknown
}

// brokenInstallHint 安装残缺时的可操作提示。
//
// 抽成函数（而不是内联字符串）是为了能被测试钉住：这段文案是用户从
// 「点了必然 500 的死按钮」走出来的唯一指引，被人改掉或删掉就又变成死路。
func brokenInstallHint() string {
	return "检测到 Docker 安装不完整（有 docker 命令但找不到 Docker Desktop 本体，" +
		"通常是上次安装中断）。请点「重新安装 Docker」重跑安装"
}

// dockerDesktopExe 定位 Windows 上的 Docker Desktop 主程序。
// 返回空串表示没装。用于两件事：把「装了但没启动过」识别成 DockerDesktop 后端，
// 以及由 StartDockerVM 真正把它拉起来（而不是让用户自己去开）。
func dockerDesktopExe() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	cands := []string{
		`C:\Program Files\Docker\Docker\Docker Desktop.exe`,
		`C:\Program Files\Docker\Docker\frontend\Docker Desktop.exe`,
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		cands = append(cands,
			filepath.Join(pf, "Docker", "Docker", "Docker Desktop.exe"),
			filepath.Join(pf, "Docker", "Docker", "frontend", "Docker Desktop.exe"),
		)
	}
	for _, p := range cands {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// DockerRuntime 返回运行时规格快照（含推荐值与是否欠配）。
func (c *Center) DockerRuntime() DockerRuntimeInfo {
	info := DockerRuntimeInfo{
		Backend: c.detectDockerBackend(),
		Present: c.dockerBinaryPresent(),
	}
	info.HostCores, info.HostMemMB = hostSpecs()
	info.DaemonOK = info.Present && c.daemonAlive()

	info.DesktopInstalled = dockerDesktopExe() != "" ||
		(runtime.GOOS == "darwin" && dockerDesktopSettingsPath() != "")

	if info.DaemonOK {
		if cores, memMB := c.dockerEngineSpecs(); cores > 0 {
			info.VMCores, info.VMMemMB = cores, memMB
			info.VMSpecsKnown = true // 真实探测值
		}
	}
	// 守护进程没起来时，尽量从后端配置里读已保存的规格。
	// 注意这是**回落值**：容器并不存在，VMSpecsKnown 保持 false，
	// 前端据此显示「—」而不是把它当成真实的「容器可用」。
	if info.VMCores == 0 {
		info.VMCores, info.VMMemMB = c.savedVMSpecs(info.Backend)
	}

	// 虚拟化前置条件：只在守护进程没起来时探（探测要起 PowerShell，
	// 而 DockerRuntime 被前端轮询；守护进程正常时前置条件必然已满足）。
	info.VirtOK = true
	if !info.DaemonOK && runtime.GOOS == "windows" && info.Present {
		if vs := detectWindowsVirtualization(); !vs.OK {
			info.VirtOK = false
			info.VirtReason = vs.Reason
		}
	}

	switch info.Backend {
	case BackendNativeLinux:
		// 容器直跑宿主内核：VM 规格 == 宿主规格，无可调项
		info.VMCores, info.VMMemMB = info.HostCores, info.HostMemMB
		info.Resizable = false
		info.Message = "Linux 原生 Docker：容器直接使用宿主 CPU/内存，无需也无法调整虚拟机规格。"
	case BackendColima:
		info.Resizable = true
	case BackendDockerDesktop:
		info.Resizable = true
		// 虚拟化缺失是真正的拦路石，优先于「改规格要重启」这种次要提示。
		// 用户截图里 Docker Desktop 自己弹的就是这个，界面必须同步说清。
		if !info.VirtOK {
			info.Message = info.VirtReason
		} else {
			info.Message = "Docker Desktop：改规格需要重启 Docker Desktop 才会生效。"
		}
	default:
		info.Resizable = false
		if !info.Present {
			info.Message = "未检测到 Docker。"
		} else {
			// 有 docker 命令但认不出运行时：安装残骸。给可操作出路而不是
			// 「请在对应客户端里手动调整」这种用户无从下手的话。
			info.Broken = true
			info.Message = "检测到 Docker 安装不完整：有 docker 命令但找不到运行时本体（Docker Desktop / colima），" +
				"通常是上次安装中断留下的。建议点「重新安装 Docker」重跑一次安装。"
		}
	}

	// Startable：本程序能不能把它拉起来。colima 与 Docker Desktop 都能；
	// Linux 原生要动 systemd，不在这里代劳。
	switch info.Backend {
	case BackendColima:
		info.Startable = info.Present
	case BackendDockerDesktop:
		// VirtOK 必须参与判定：虚拟化没开时 Docker Desktop 起不来，
		// 给个「启动 Docker」按钮只会让用户白等 4 分钟超时。
		info.Startable = runtime.GOOS != "linux" && info.VirtOK &&
			(dockerDesktopExe() != "" || runtime.GOOS == "darwin")
	}

	info.RecCores, info.RecMemMB = recommendVMSpecs(info.HostCores, info.HostMemMB)
	info.CurSlots = capacityFromSpecs(info.VMCores, info.VMMemMB)
	info.RecSlots = capacityFromSpecs(info.RecCores, info.RecMemMB)
	// 欠配判定：可调、且推荐槽位比当前多 50% 以上（避免为了 1 个槽位就提示重建 VM）
	info.Undersized = info.Resizable && info.VMCores > 0 &&
		info.RecSlots >= info.CurSlots*3/2 && info.RecSlots > info.CurSlots

	// 推荐值只取本机 2/3 的核，所以用户手动给过更高配额时，推荐值会**低于**当前。
	// 这种情况下它不是「优化」而是降配，必须让前端能区分，否则会把
	// 「18 槽 → 12 槽」当成提升展示出来。
	info.RecIsUpgrade = info.RecSlots > info.CurSlots
	if info.Resizable && info.VMCores > 0 && info.RecSlots < info.CurSlots {
		info.AboveRecommended = true
		if info.Message == "" {
			info.Message = fmt.Sprintf(
				"当前配额（%d 核）已高于推荐值（%d 核，本机 %d 核的 2/3）。"+
					"这样打码更快，但注册期间系统可用核会更少；如需让出资源可手动调低。",
				info.VMCores, info.RecCores, info.HostCores)
		}
	}

	return info
}

// savedVMSpecs 从后端自身的配置文件读规格（守护进程没起来时的兜底）。
func (c *Center) savedVMSpecs(backend DockerBackend) (cores, memMB int) {
	switch backend {
	case BackendColima:
		return colimaSavedSpecs()
	case BackendDockerDesktop:
		return dockerDesktopSavedSpecs()
	}
	return 0, 0
}

// ---- colima ----

// colimaSavedSpecs 读 colima 自己保存的 profile 规格。
func colimaSavedSpecs() (cores, memMB int) {
	out, err := procutil.Command("colima", "list", "--json").Output()
	if err != nil {
		return 0, 0
	}
	// colima list --json 每行一个 profile
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p struct {
			Name   string `json:"name"`
			CPUs   int    `json:"cpus"`
			Memory int64  `json:"memory"` // bytes
		}
		if json.Unmarshal([]byte(line), &p) != nil {
			continue
		}
		if p.Name == "default" || cores == 0 {
			cores = p.CPUs
			memMB = int(p.Memory / (1024 * 1024))
		}
	}
	return cores, memMB
}

// colimaProfileExists 判断 colima 是否已有实例（无论运行/停止）。
// 用于安装路径的启动决策：实例已存在时绝不能用带规格 flag 的
// `colima start --cpu/--memory`（--save-config 默认 true 会覆写用户配额）。
func (c *Center) colimaProfileExists() bool {
	out, err := procutil.Command("colima", "list", "--json").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(line), &p) == nil && p.Name != "" {
			return true
		}
	}
	return false
}

// ---- Docker Desktop ----

// dockerDesktopSettingsPath 返回 Docker Desktop 的 settings 文件路径（不存在返回 ""）。
func dockerDesktopSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Group Containers", "group.com.docker")
		cands = []string{
			filepath.Join(base, "settings-store.json"), // 4.30+
			filepath.Join(base, "settings.json"),       // 旧版
		}
	case "windows":
		base := filepath.Join(home, "AppData", "Roaming", "Docker")
		cands = []string{
			filepath.Join(base, "settings-store.json"),
			filepath.Join(base, "settings.json"),
		}
	}
	for _, p := range cands {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// dockerDesktopSavedSpecs 读 Docker Desktop settings 里的 CPU/内存配额。
func dockerDesktopSavedSpecs() (cores, memMB int) {
	p := dockerDesktopSettingsPath()
	if p == "" {
		return 0, 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, 0
	}
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		return 0, 0
	}
	num := func(keys ...string) int {
		for _, k := range keys {
			if v, ok := s[k]; ok {
				switch t := v.(type) {
				case float64:
					return int(t)
				case string:
					if n, e := strconv.Atoi(t); e == nil {
						return n
					}
				}
			}
		}
		return 0
	}
	return num("cpus", "Cpus", "cpu"), num("memoryMiB", "MemoryMiB", "memoryMib")
}

// ApplyDockerVMSpecs 把 Docker VM 调整到指定规格。
// cores/memMB <=0 时用推荐值。这是重量级操作：会重启 Docker 运行时，
// 期间所有容器不可用（重启策略为 always/unless-stopped 的会自行回来）。
func (c *Center) ApplyDockerVMSpecs(cores, memMB int, logf func(string)) error {
	log := func(s string) {
		if logf != nil {
			logf(s)
		}
	}
	info := c.DockerRuntime()
	if cores <= 0 {
		cores = info.RecCores
	}
	if memMB <= 0 {
		memMB = info.RecMemMB
	}
	if info.HostCores > 0 && cores > info.HostCores {
		return fmt.Errorf("核数 %d 超过宿主的 %d 核", cores, info.HostCores)
	}
	if info.HostMemMB > 0 && memMB > info.HostMemMB {
		return fmt.Errorf("内存 %dMB 超过宿主的 %dMB", memMB, info.HostMemMB)
	}
	if cores < 1 || memMB < 1024 {
		return fmt.Errorf("规格过低（%d 核 / %dMB），至少 1 核 1024MB", cores, memMB)
	}

	switch info.Backend {
	case BackendColima:
		return c.applyColimaSpecs(cores, memMB, log)
	case BackendDockerDesktop:
		return c.applyDockerDesktopSpecs(cores, memMB, log)
	case BackendNativeLinux:
		return fmt.Errorf("Linux 原生 Docker 无虚拟机层，容器已直接使用宿主全部 CPU/内存，无需调整")
	default:
		return fmt.Errorf("无法识别 Docker 运行方式，请在对应客户端里手动调整规格")
	}
}

// applyColimaSpecs 用 colima stop + start --cpu/--memory 重建 VM 规格。
// colima 不支持在线改规格，必须停机重启（磁盘与镜像保留）。
func (c *Center) applyColimaSpecs(cores, memMB int, log func(string)) error {
	if _, err := exec.LookPath("colima"); err != nil {
		return fmt.Errorf("未找到 colima 可执行文件")
	}
	memGB := memMB / 1024
	if memGB < 1 {
		memGB = 1
	}
	log(fmt.Sprintf("正在停止 colima（容器会短暂中断，重启策略为 always/unless-stopped 的会自动回来）…"))
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if out, err := procutil.CommandContext(ctx, "colima", "stop").CombinedOutput(); err != nil {
		log(strings.TrimSpace(string(out)))
		return fmt.Errorf("colima stop 失败: %v", err)
	}
	log(fmt.Sprintf("正在以 %d 核 / %dGB 启动 colima…", cores, memGB))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 420*time.Second)
	defer cancel2()
	out, err := procutil.CommandContext(ctx2, "colima", "start",
		"--cpu", strconv.Itoa(cores), "--memory", strconv.Itoa(memGB)).CombinedOutput()
	if err != nil {
		log(strings.TrimSpace(string(out)))
		return fmt.Errorf("colima start 失败: %v", err)
	}
	c.invalidateSpecCache()
	log("colima 已按新规格启动")
	return nil
}

// StartDockerVM 只处理「装了 Docker 但守护进程连不上」这一种情况：
//   - colima 实例停了        → 裸 colima start（不带 --cpu/--memory：colima 的
//     --save-config 默认 true，带规格 flag 会把用户已调好的 12C/16G 永久覆写成默认值）
//   - 实例在跑但宿主 socket 僵尸 → colima restart（colima start 会提示 already running）
//   - 守护进程本来就好        → 无操作直接返回
//
// 这是「切换 docker 打码直接报错」的修复核心：之前前端把 docker_ok=false 一律当
// 「未安装」去走全自动重装，colima 的配置就是这样被反复覆写/拆掉的。
func (c *Center) StartDockerVM() (string, error) {
	if c.daemonAlive() {
		return "Docker 守护进程已就绪", nil
	}
	if !c.dockerBinaryPresent() {
		return "", fmt.Errorf("未检测到 Docker，请先安装（Docker 管理页提供全自动安装）")
	}
	if runtime.GOOS == "linux" {
		return "", fmt.Errorf("Linux 原生 Docker 没有虚拟机，请检查 docker 服务：systemctl status docker")
	}
	switch c.detectDockerBackend() {
	case BackendColima:
		if _, err := exec.LookPath("colima"); err != nil {
			return "", fmt.Errorf("未找到 colima 可执行文件")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
		defer cancel()
		out, err := procutil.CommandContext(ctx, "colima", "start").CombinedOutput()
		low := strings.ToLower(string(out))
		// colima 对已运行的实例会提示 "already running"（exit 0 或非 0 都见过），
		// 此时宿主 socket 仍可能连不上（僵尸转发）——唯一正确动作是 restart。
		if err != nil && !strings.Contains(low, "already running") {
			return "", fmt.Errorf("colima start 失败：%v / %s", err, trunc(string(out), 300))
		}
		if !c.daemonAlive() {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 240*time.Second)
			defer cancel2()
			out2, err2 := procutil.CommandContext(ctx2, "colima", "restart").CombinedOutput()
			if err2 != nil {
				return "", fmt.Errorf("colima restart 失败：%v / %s", err2, trunc(string(out2), 300))
			}
		}
		if c.daemonAlive() {
			// 守护进程刚从「连不上」变成「可用」：立刻丢弃 DockerAvailable 缓存，
			// 否则前端下一拍拿到的仍是缓存里的 false，用户会以为启动没生效。
			c.invalidateDockerAvail()
			return "colima 虚拟机已就绪，Docker 可用", nil
		}
		return "", fmt.Errorf("colima 已启动但守护进程仍未就绪，请稍后重试")
	case BackendDockerDesktop:
		// Windows/macOS 上「启动虚拟机」= 拉起 Docker Desktop 本体。
		// 旧实现只丢一句「请手动打开」，等于这个按钮在 Windows 上毫无用处。
		return c.startDockerDesktop()
	default:
		// Windows 上还有一种状态：docker.exe 在（present=true）但既不是 colima
		// 也找不到 Docker Desktop —— 通常是安装中断留下的残骸。这时候正确的
		// 出路是重新安装，而不是报「无法识别」让用户卡死在这一步。
		if runtime.GOOS == "windows" {
			return "", fmt.Errorf("%s", brokenInstallHint())
		}
		return "", fmt.Errorf("无法识别 Docker 运行方式，请检查 Docker 客户端状态")
	}
}

// startDockerDesktop 拉起 Docker Desktop 并等守护进程就绪。
//
// Windows 上首次启动要初始化 WSL2 后端，实测 60~180 秒；macOS 上 30~120 秒。
// 所以这里等到 240 秒，中途每次探测走 daemonAlive()（不读缓存）。
func (c *Center) startDockerDesktop() (string, error) {
	switch runtime.GOOS {
	case "windows":
		exe := dockerDesktopExe()
		if exe == "" {
			return "", fmt.Errorf("找不到 Docker Desktop 本体（请确认已安装）")
		}
		// 前置条件不满足就立刻返回，不要拉起来再等 4 分钟超时 ——
		// Docker Desktop 会自己弹 "Virtualization support not detected"，
		// 而我们的进度面板干等到超时才报错，用户以为是本程序卡住了。
		if vs := detectWindowsVirtualization(); !vs.OK {
			return "", fmt.Errorf("%s", vs.Reason)
		}
		// 用 Start-Process 而不是直接 exec：Docker Desktop 是 GUI 程序，
		// 直接 exec 会让它成为本进程的子进程，UmbraForge 退出时可能被一起带走。
		ps := fmt.Sprintf(`Start-Process -FilePath %s -WindowStyle Minimized`, psQuote(exe))
		if out, err := procutil.Command("powershell", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", ps).CombinedOutput(); err != nil {
			return "", fmt.Errorf("启动 Docker Desktop 失败：%v / %s", err, trunc(string(out), 200))
		}
	case "darwin":
		app := "/Applications/Docker.app"
		if !fileExists(app) {
			app = "/Applications/Docker Desktop.app"
		}
		if !fileExists(app) {
			return "", fmt.Errorf("找不到 Docker Desktop 应用")
		}
		if out, err := procutil.Command("open", "-g", "-a", app).CombinedOutput(); err != nil {
			return "", fmt.Errorf("启动 Docker Desktop 失败：%v / %s", err, trunc(string(out), 200))
		}
	default:
		return "", fmt.Errorf("当前平台不支持自动启动 Docker Desktop")
	}

	deadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(deadline) {
		if c.daemonAlive() {
			c.invalidateDockerAvail()
			c.invalidateSpecCache()
			return "Docker Desktop 已就绪", nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("Docker Desktop 已启动但守护进程未在 4 分钟内就绪。" +
		"首次启动需初始化 WSL2 后端，可稍等后点「刷新」；若一直不就绪，" +
		"请打开 Docker Desktop 查看它自己的错误提示（常见原因：WSL2 未安装或虚拟化未在 BIOS 中开启）")
}

// applyDockerDesktopSpecs 改 Docker Desktop 的 settings 文件并重启客户端。
func (c *Center) applyDockerDesktopSpecs(cores, memMB int, log func(string)) error {
	p := dockerDesktopSettingsPath()
	if p == "" {
		return fmt.Errorf("找不到 Docker Desktop 配置文件，请在其 Settings → Resources 里手动调整")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("读取 Docker Desktop 配置失败: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("解析 Docker Desktop 配置失败: %v", err)
	}
	// 备份一份，改坏了用户还能回滚
	backup := p + ".umbraforge.bak"
	if !fileExists(backup) {
		_ = os.WriteFile(backup, b, 0o644)
		log("已备份原配置到 " + filepath.Base(backup))
	}
	setIfPresentOrNew(s, cores, "cpus", "Cpus")
	setIfPresentOrNew(s, memMB, "memoryMiB", "MemoryMiB")
	nb, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, nb, 0o644); err != nil {
		return fmt.Errorf("写入 Docker Desktop 配置失败: %v", err)
	}
	log(fmt.Sprintf("已写入 %d 核 / %dMB 到 %s", cores, memMB, filepath.Base(p)))
	c.invalidateSpecCache()
	if err := c.restartDockerDesktop(log); err != nil {
		return fmt.Errorf("配置已写入，但自动重启 Docker Desktop 失败（手动重启即可生效）: %v", err)
	}
	return nil
}

// setIfPresentOrNew 优先改已存在的键（兼容不同版本的大小写），都不存在则写第一个。
func setIfPresentOrNew(s map[string]any, val int, keys ...string) {
	for _, k := range keys {
		if _, ok := s[k]; ok {
			s[k] = val
			return
		}
	}
	if len(keys) > 0 {
		s[keys[0]] = val
	}
}

// restartDockerDesktop 重启 Docker Desktop 并等守护进程回来。
func (c *Center) restartDockerDesktop(log func(string)) error {
	log("正在重启 Docker Desktop…")
	switch runtime.GOOS {
	case "darwin":
		_ = procutil.Command("osascript", "-e", `quit app "Docker Desktop"`).Run()
		_ = procutil.Command("osascript", "-e", `quit app "Docker"`).Run()
		time.Sleep(6 * time.Second)
		if err := procutil.Command("open", "-a", "Docker").Start(); err != nil {
			return err
		}
	case "windows":
		_ = procutil.Command("taskkill", "/IM", "Docker Desktop.exe", "/F").Run()
		time.Sleep(6 * time.Second)
		exe := `C:\Program Files\Docker\Docker\Docker Desktop.exe`
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			if p := filepath.Join(pf, "Docker", "Docker", "Docker Desktop.exe"); fileExists(p) {
				exe = p
			}
		}
		if err := procutil.Command(exe).Start(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported OS")
	}
	// 等守护进程回来（Docker Desktop 冷启动通常 30~90 秒）
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		if c.daemonAlive() {
			log("Docker Desktop 已就绪")
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("Docker Desktop 在 180 秒内未就绪")
}

// taskKeyVMResize 是调整 VM 规格的异步任务 key。
const taskKeyVMResize = "docker-vm-resize"

// ApplyDockerVMSpecsAsync 异步调整规格。colima 停机+重启常要 1~3 分钟，
// Docker Desktop 冷启动更久，HTTP 不能同步等，前端轮询 docker-task 看进度。
func (c *Center) ApplyDockerVMSpecsAsync(cores, memMB int) PluginTask {
	key := taskKeyVMResize
	c.mu.Lock()
	if t := c.tasks[key]; t != nil && t.Running {
		snap := *t
		c.mu.Unlock()
		return snap
	}
	t := &PluginTask{
		Key:     key,
		Running: true,
		Stage:   "resize",
		Message: "正在调整 Docker 虚拟机规格…",
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Started: time.Now(),
		Updated: time.Now(),
	}
	c.tasks[key] = t
	c.mu.Unlock()

	// 记下调整前的槽位，完成后好告诉用户「提升了多少」
	oldSlots := c.DockerRuntime().CurSlots

	go func() {
		if err := c.ApplyDockerVMSpecs(cores, memMB, func(s string) { c.taskLog(key, s) }); err != nil {
			c.taskFinish(key, false, "failed", err.Error())
			return
		}
		info := c.DockerRuntime()
		// 每槽约 20 秒一次挑战 → 每分钟约 3 次
		c.taskFinish(key, true, "ready", fmt.Sprintf(
			"已调整为 %d 核 / %dGB —— 打码槽位 %d → %d，理论吞吐约 %d → %d 个/分钟",
			info.VMCores, info.VMMemMB/1024, oldSlots, info.CurSlots,
			oldSlots*3, info.CurSlots*3))
	}()
	return *t
}

// invalidateSpecCache 规格变了要让缓存立刻失效，否则钳制还按旧核数算。
// 规格变更必然伴随守护进程重启，所以顺带丢弃 DockerAvailable 缓存 ——
// 否则「虚拟机刚起好」这一拍仍会被报成不可用。
func (c *Center) invalidateSpecCache() {
	c.specMu.Lock()
	c.specCores, c.specMemMB, c.specAt = 0, 0, time.Time{}
	c.specMu.Unlock()
	c.invalidateDockerAvail()
}
