package plugins

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/umbraforge/desktop/internal/procutil"
)

// ── Windows 上安装 Docker Desktop ──
//
// 原实现是一行 `winget install -e --id Docker.DockerDesktop`，在 Windows 上
// 基本装不上，有四个独立原因：
//
//  1. **没有提权**。Docker Desktop 的 MSI 必须管理员权限才能装。GUI 启动的
//     UmbraForge 是普通用户权限，winget 会直接失败（或弹一个我们收不到回应的
//     UAC 而后超时）。必须走 ShellExecute runas 显式提权。
//  2. **没有静默旗标**。缺 --silent 时 Docker Desktop 安装器会弹自己的 GUI
//     向导，用户以为「程序卡住了」。
//  3. **winget 找不到**。winget 是 App Execution Alias（WindowsApps 下的零字节
//     重解析点）。从 GUI 启动的进程 PATH 里不一定有 WindowsApps，
//     exec.LookPath 会失败，于是报「未检测到 winget」而其实装着。
//  4. **退出码被当成失败**。winget 有几个非零码其实是成功语义
//     （已安装 / 需重启），旧代码一律报错。
//
// 提权后无法直通 stdout（ShellExecute 起的是独立进程），所以让 winget 把日志
// 写到临时文件，这里边等边 tail，进度照样能实时显示。
//
// 关于「静默」：UAC 确认框无法也不应绕过 —— 装系统级软件必须有用户同意。
// 这里做到的是「除了那一次 UAC，不再有任何终端窗口或安装向导」。

// wingetPath 定位 winget.exe。
//
// 先试 PATH；失败再查 WindowsApps 的固定位置。后者是必需的兜底：
// App Execution Alias 是零字节重解析点，且 GUI 启动的进程环境里
// 常常没有 %LOCALAPPDATA%\Microsoft\WindowsApps。
func wingetPath() string {
	if p, err := exec.LookPath("winget"); err == nil && strings.TrimSpace(p) != "" {
		return p
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		if up := os.Getenv("USERPROFILE"); up != "" {
			local = filepath.Join(up, "AppData", "Local")
		}
	}
	cands := []string{}
	if local != "" {
		cands = append(cands, filepath.Join(local, "Microsoft", "WindowsApps", "winget.exe"))
	}
	// Windows Server / 手工装 App Installer 的场景
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		cands = append(cands,
			filepath.Join(pf, "WindowsApps", "winget.exe"),
			filepath.Join(pf, "Microsoft", "WindowsApps", "winget.exe"),
		)
	}
	for _, p := range cands {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// wingetExitOK 判断 winget 退出码是否属于「成功」语义。
//
// winget 用高位十六进制码表达细分结果，其中这几个不是失败：
//
//	0x8A15002B APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE —— 已是最新/已安装
//	0x8A150011 ...PACKAGE_ALREADY_INSTALLED               —— 已安装
//	0x8A150109 ...INSTALL_REBOOT_REQUIRED_TO_FINISH        —— 装好了，待重启
//	0x8A150077 ...INSTALL_REBOOT_REQUIRED_FOR_INSTALL      —— 需重启后继续
//
// 旧代码把这些一律当失败，于是「其实已经装好」也报安装失败。
// 返回 needReboot 让调用方给出「重启后 Docker 才可用」的准确提示。
func wingetExitOK(code int) (ok bool, needReboot bool) {
	switch uint32(code) {
	case 0:
		return true, false
	case 0x8A15002B, 0x8A150011:
		return true, false
	case 0x8A150109, 0x8A150077:
		return true, true
	}
	return false, false
}

// wingetExitHint 把常见失败码翻译成可操作提示，避免只丢一个十六进制数给用户。
func wingetExitHint(code int) string {
	switch uint32(code) {
	case 0x8A150014:
		return "未找到 Docker.DockerDesktop 包（winget 源可能未初始化，可先手动执行一次 winget source update）"
	case 0x8A15010D:
		return "安装被用户取消（UAC 未确认）"
	case 0x8A150044:
		return "需要管理员权限。请在 UAC 提示中选择「是」"
	case 0x8A150045:
		return "winget 源不可用（网络或 Microsoft Store 服务异常）"
	}
	return fmt.Sprintf("winget 退出码 0x%X", uint32(code))
}

// installDockerWindows 提权 + 静默安装 Docker Desktop，并把 winget 日志实时
// 转进任务进度。返回 needReboot 表示装好了但要重启才能用。
func (c *Center) installDockerWindows(t *PluginTask) (needReboot bool, err error) {
	wg := wingetPath()
	if wg == "" {
		return false, fmt.Errorf("未检测到 winget（App Installer）。请从 Microsoft Store 安装「应用安装程序」后重试，" +
			"或手动下载 Docker Desktop：https://www.docker.com/products/docker-desktop/")
	}
	c.taskLog(t.Key, "winget: "+wg)

	// --silent 抑制 Docker Desktop 自己的安装向导；
	// --accept-*-agreements 免交互；--disable-interactivity 禁止 winget 反问。
	args := []string{
		"install", "-e", "--id", "Docker.DockerDesktop",
		"--silent",
		"--accept-source-agreements", "--accept-package-agreements",
		"--disable-interactivity",
	}

	c.taskStage(t.Key, "install", "正在安装 Docker Desktop（winget 会请求管理员授权，请在 UAC 提示中选择「是」）…")
	c.taskLog(t.Key, "$ winget "+strings.Join(args, " ")+"  （流式 · 安装器自行请求提权）")

	code, err := c.runWingetStreamed(t, wg, args)
	if err != nil {
		return false, err
	}
	ok, reboot := wingetExitOK(code)
	if !ok {
		return false, fmt.Errorf("%s", wingetExitHint(code))
	}
	// 装完必须验证本体真的落地：winget 的「已安装」成功码可能是上次卸载
	// 残留的记录（实测：返回成功但 Program Files\\Docker 只有空壳目录），
	// 此时继续等守护进程必然 180 秒超时。文件在才算装成功。
	if !reboot && dockerDesktopExe() == "" {
		return false, fmt.Errorf("winget 报告安装成功，但未找到 Docker Desktop 本体" +
			"（C:\\Program Files\\Docker\\Docker\\Docker Desktop.exe）。这通常是上次卸载残留的记录，" +
			"请重试；仍失败请手动安装 https://www.docker.com/products/docker-desktop/")
	}
	if reboot {
		c.taskLog(t.Key, "Docker Desktop 已安装，但需要重启系统才能启用（WSL2/Hyper-V 组件）")
	}
	return reboot, nil
}

// sanitizeDockerCredsStore 清理 ~/.docker/config.json 里指向已不存在凭据
// 助手的 credsStore 残留。
//
// 实测教训（2026-08）：Docker Desktop 卸载重装后，config.json 仍残留
// "credsStore": "desktop"，但 docker-credential-desktop.exe 可能已不在
// PATH —— 此时任何 docker pull 都直接失败：
//
//	error getting credentials - err: exec: "docker-credential-desktop":
//	executable file not found in %PATH%
//
// auths 为空（未登录任何私有仓库）时凭据助手没有任何用途，直接移除该字段。
// 三平台通用；有真实登录凭据时不动（避免破坏用户配置）。
func sanitizeDockerCredsStore() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	p := filepath.Join(home, ".docker", "config.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return
	}
	if auths, _ := cfg["auths"].(map[string]any); len(auths) != 0 {
		return // 有登录凭据时不乱动
	}
	if _, ok := cfg["credsStore"]; !ok {
		return
	}
	delete(cfg, "credsStore")
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, out, 0o600)
}

// CleanDockerInstallerCaches 导出清理入口（HTTP 层调用）。
func (c *Center) CleanDockerInstallerCaches(keepOne bool) (int, float64) {
	return cleanDockerInstallerCaches(keepOne)
}

// cleanDockerInstallerCaches 删除 Docker 安装器的重复下载缓存。
//
// winget 每次安装都会重新下载 Docker Desktop Installer（几十到几百 MB），
// 下载产物留在 %LOCALAPPDATA%\Temp\WinGet 与 DockerDesktopInstallers 下；
// 反复安装/卸载会在磁盘上堆出多份副本。keepOne=true 时保留最新一份
// （供下次安装复用语义），否则全部删除（卸载后清理）。返回删除文件数与
// 释放的 MB。
func cleanDockerInstallerCaches(keepOne bool) (removed int, freedMB float64) {
	files := findDockerInstallerCaches()
	if len(files) == 0 {
		return 0, 0
	}
	// 按修改时间升序，最后一个是"最新"；keepOne 时跳过它
	drop := files
	if keepOne && len(files) > 1 {
		drop = files[:len(files)-1]
	}
	for _, f := range drop {
		path, _ := f["path"].(string)
		if path == "" {
			continue
		}
		if os.Remove(path) == nil {
			removed++
			if mb, ok := f["size_mb"].(float64); ok {
				freedMB += mb
			}
		}
	}
	return removed, freedMB
}

// findDockerInstallerCaches 扫描已知的 Docker 安装器下载缓存位置，
// 按修改时间升序返回 [{path,size_mb,modified}]。三平台各扫各的：
//   - Windows: winget 缓存 + DockerDesktopInstallers + Temp
//   - macOS:   ~/Downloads 的 Docker.dmg + Homebrew 下载缓存
//   - Linux:   apt/dnf 包缓存里的 docker*.deb / docker*.rpm
func findDockerInstallerCaches() []map[string]any {
	var out []map[string]any
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			return
		}
		out = append(out, map[string]any{
			"path":     p,
			"size_mb":  float64(st.Size()) / 1024 / 1024,
			"modified": st.ModTime().Format("2006-01-02 15:04:05"),
			"_mtime":   st.ModTime(),
		})
	}
	walk := func(root string, pred func(name string) bool) {
		if root == "" {
			return
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if pred(filepath.Base(p)) {
				add(p)
			}
			return nil
		})
	}
	isDockerInstaller := func(name string) bool {
		lower := strings.ToLower(name)
		return strings.Contains(lower, "docker") &&
			(strings.Contains(lower, "installer") || strings.HasSuffix(lower, ".dmg") ||
				strings.HasSuffix(lower, ".deb") || strings.HasSuffix(lower, ".rpm"))
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		winGet := filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp", "WinGet")
		walk(winGet, func(name string) bool {
			lower := strings.ToLower(name)
			return strings.Contains(lower, "docker") && strings.Contains(lower, "installer") && strings.HasSuffix(lower, ".exe")
		})
		instDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp", "DockerDesktopInstallers")
		walk(instDir, func(name string) bool {
			lower := strings.ToLower(name)
			return strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".msi")
		})
		walk(filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp"), func(name string) bool {
			lower := strings.ToLower(name)
			return strings.HasPrefix(lower, "dockerdesktopinstaller") && strings.HasSuffix(lower, ".exe")
		})
	case "darwin":
		// 用户下载的 Docker.dmg / Docker Desktop.pkg + Homebrew cask 下载缓存
		walk(filepath.Join(home, "Downloads"), isDockerInstaller)
		walk(filepath.Join(home, "Library", "Caches", "Homebrew", "downloads"), func(name string) bool {
			lower := strings.ToLower(name)
			return strings.Contains(lower, "docker") && (strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg"))
		})
		walk(filepath.Join(home, "Library", "Application Support", "Docker Desktop"), func(name string) bool {
			lower := strings.ToLower(name)
			return strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg")
		})
	case "linux":
		// apt / dnf 包缓存（普通用户可读）
		walk("/var/cache/apt/archives", func(name string) bool {
			lower := strings.ToLower(name)
			return strings.HasPrefix(lower, "docker") && strings.HasSuffix(lower, ".deb")
		})
		walk("/var/cache/dnf", func(name string) bool {
			lower := strings.ToLower(name)
			return strings.HasPrefix(lower, "docker") && strings.HasSuffix(lower, ".rpm")
		})
	}
	sort.Slice(out, func(i, j int) bool {
		mi, _ := out[i]["_mtime"].(time.Time)
		mj, _ := out[j]["_mtime"].(time.Time)
		return mi.Before(mj)
	})
	// _mtime 仅内部排序用，不暴露给前端
	for _, f := range out {
		delete(f, "_mtime")
	}
	return out
}

// runWingetStreamed 直接运行 winget 并把输出实时转进任务进度，返回退出码。
//
// 为什么不用 Start-Process -Verb RunAs 预提权：实测（2026-08）预提权路径
// 在「卸载后重装」场景返回成功码但 Docker Desktop 文件根本没落地 ——
// 提权后的 winget 再触发安装器自身的 UAC 时交互链断掉，静默无产出。
// winget 安装 Docker Desktop 时**自己会请求管理员**（"安装程序将请求以
// 管理员身份运行"），普通权限直接跑即可，UAC 照常弹一次，行为可靠。
func (c *Center) runWingetStreamed(t *PluginTask, wingetExe string, args []string) (int, error) {
	cmd := procutil.Command(wingetExe, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("winget stderr pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("winget stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("启动 winget 失败：%v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.pipeWingetLines(t, stdout) }()
	go func() { defer wg.Done(); c.pipeWingetLines(t, stderr) }()
	werr := <-done
	wg.Wait() // 等两个管道读完再返回，日志不丢尾
	code := 0
	if werr != nil {
		code = exitCodeOf(werr)
	}
	return code, nil
}

// pipeWingetLines 把 winget 输出流逐行转进任务日志，过滤进度条装饰行。
func (c *Center) pipeWingetLines(t *PluginTask, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// winget 的进度条是一长串 ██/─ 字符，转进日志只会刷屏
		if strings.ContainsAny(line, "█▒░─") && len(strings.TrimLeft(line, "█▒░─ %0123456789.MBKGiBs/")) == 0 {
			continue
		}
		c.taskLog(t.Key, line)
	}
}

// drainInstallLog 把日志文件里尚未上报的字节转成任务日志行，返回新的偏移。
// winget 会输出进度条回车（\r），这里按 \r\n 一起切并丢掉纯装饰行。
func (c *Center) drainInstallLog(t *PluginTask, path string, from int) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) <= from {
		return from
	}
	chunk := string(b[from:])
	for _, raw := range strings.FieldsFunc(chunk, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// winget 的进度条是一长串 ██/─ 字符，转进日志只会刷屏
		if strings.ContainsAny(line, "█▒░─") && len(strings.TrimLeft(line, "█▒░─ %0123456789.MBKGiBs/")) == 0 {
			continue
		}
		c.taskLog(t.Key, line)
	}
	return len(b)
}

// psQuote 按 PowerShell 单引号规则转义（内部单引号写两遍）。
// 不能用双引号：路径里的 $ 会被 PowerShell 当变量展开。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// exitCodeOf 从 exec 错误里取出子进程退出码。
// 取不到（比如根本没起来）时返回 -1，调用方会当作失败。
func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
