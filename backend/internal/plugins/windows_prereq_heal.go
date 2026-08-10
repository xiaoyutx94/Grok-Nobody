//go:build windows

package plugins

// ── Windows 新机器自动前置配置 ──
//
// 新电脑上「一键安装 Docker」此前会卡在三个手动环节，界面只提示不动手：
//  1. 「虚拟机平台」（VirtualMachinePlatform）功能未启用 —— Docker Desktop
//     的 WSL2 引擎起不来（实测：功能启用后不重启，hypervisor 不加载，
//     wsl --status 报「当前计算机配置不支持 WSL2」）。
//  2. WSL2 内核组件缺失（wsl --status 报 wsl2kernel 相关提示）。
//  3. Python 只是 Microsoft Store 的 0 字节占位符（App Execution Alias），
//     EzSolver 本地模式 venv 创建静默失败（退出码 9009）。
//
// 这里把三件事全部自动化：DISM 提权启用功能（一次 UAC）、WSL 组件安装、
// python.org 安装器静默装 Python 3.12（用户级安装，无需管理员）。
// 启用功能后必须重启系统 —— 返回 needReboot，调用方给出明确提示。

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/umbraforge/desktop/internal/procutil"
)

// healWindowsPrereqs 修复 Docker Desktop 在 Windows 上的宿主前置条件：
// 虚拟机平台功能 + WSL2 组件。返回 needReboot 表示已启用需要重启的组件，
// 调用方应停在「重启后继续」而不是继续装 Docker（装了也起不来）。
func (c *Center) healWindowsPrereqs(t *PluginTask) (needReboot bool) {
	// 1) 「虚拟机平台」功能（Docker Desktop WSL2 引擎的硬前置）
	if !vmPlatformFeatureEnabled() {
		t.Stage = "prereq"
		t.Message = "正在启用 Windows「虚拟机平台」功能（请在 UAC 提示中选择「是」）…"
		c.taskLog(t.Key, "$ dism /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart  （已提权）")
		code, err := c.runElevated(t, "dism.exe", []string{
			"/online", "/enable-feature", "/featurename:VirtualMachinePlatform", "/all", "/norestart",
		})
		if err != nil {
			c.taskLog(t.Key, "DISM 提权启动失败："+err.Error()+"（可手动以管理员运行 dism /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart）")
			return false
		}
		if code != 0 {
			c.taskLog(t.Key, fmt.Sprintf("DISM 返回码 %d（0x%X），功能可能未启用（可手动重试）", code, uint32(code)))
			return false
		}
		needReboot = true
		c.taskLog(t.Key, "「虚拟机平台」已启用（重启系统后生效）")
	} else {
		c.taskLog(t.Key, "「虚拟机平台」功能已启用")
	}

	// 2) WSL2 组件（内核/发行版支持）。store 版 WSL 的 --update 与
	// --install --no-distribution 普通权限即可运行，无需提权。
	if !wsl2Available() {
		t.Message = "正在安装/更新 WSL2 组件…"
		c.taskLog(t.Key, "$ wsl --update")
		if out, err := procutil.Command("wsl.exe", "--update").CombinedOutput(); err != nil {
			c.taskLog(t.Key, "wsl --update 失败："+trunc(string(out), 200))
		}
		c.taskLog(t.Key, "$ wsl --install --no-distribution")
		if out, err := procutil.Command("wsl.exe", "--install", "--no-distribution").CombinedOutput(); err != nil {
			c.taskLog(t.Key, "wsl --install --no-distribution 失败："+trunc(string(out), 200))
		}
		if wsl2Available() {
			c.taskLog(t.Key, "WSL2 组件就绪")
		} else {
			c.taskLog(t.Key, "WSL2 组件安装后仍不可用：需要重启系统，或检查 BIOS 是否开启 CPU 虚拟化（Intel VT-x / AMD-V）")
			needReboot = true
		}
	} else {
		c.taskLog(t.Key, "WSL2 已就绪")
	}
	return needReboot
}

// ensureWindowsPython 确保存在可用的真实 Python（python.org 安装），
// 返回可执行文件绝对路径。触发场景：PATH 里只有 Microsoft Store 的
// 0 字节占位符（App Execution Alias），或压根没装 Python。
// 下载 python.org 3.12 安装器并**用户级静默安装**（InstallAllUsers=0，
// 不需要管理员权限、不弹 UAC），装到 %LOCALAPPDATA%\Programs\Python。
func ensureWindowsPython(logPath string) (string, error) {
	logf := func(s string) { _ = appendFile(logPath, s+"\n") }
	if p := findWindowsPython(); p != "" {
		logf("已检测到真实 Python: " + p)
		return p, nil
	}
	const ver = "3.12.10"
	url := "https://www.python.org/ftp/python/" + ver + "/python-" + ver + "-amd64.exe"
	dst := filepath.Join(os.TempDir(), "python-"+ver+"-amd64.exe")
	logf("未检测到真实 Python，下载 " + url)
	if err := downloadFile(url, dst, logf); err != nil {
		return "", fmt.Errorf("下载 Python 安装器失败: %v", err)
	}
	args := []string{
		"/quiet",
		"InstallAllUsers=0",
		"PrependPath=1",
		"Include_launcher=1",
		"Include_pip=1",
		"Include_test=0",
		"Include_doc=0",
	}
	logf("$ " + dst + " " + strings.Join(args, " ") + "  （用户级静默安装，无需 UAC）")
	if out, err := procutil.Command(dst, args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("Python 静默安装失败: %v / %s", err, trunc(string(out), 300))
	}
	_ = os.Remove(dst)
	if p := findWindowsPython(); p != "" {
		logf("Python 安装完成: " + p)
		return p, nil
	}
	// 注册表延迟写入的兜底：直接探测默认安装位置
	la := os.Getenv("LOCALAPPDATA")
	if la != "" {
		probe := filepath.Join(la, "Programs", "Python", "Python312", "python.exe")
		if fileExists(probe) {
			logf("Python 安装完成（路径探测）: " + probe)
			return probe, nil
		}
	}
	return "", fmt.Errorf("Python 安装完成但未找到可执行文件，请手动安装 https://www.python.org/downloads/")
}

// downloadFile 下载文件到 dst，带 10MB 进度日志与 5 分钟超时。
func downloadFile(url, dst string, logf func(string)) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 1<<20) // 1MB
	last := time.Now()
	total := int64(0)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			total += int64(n)
			if time.Since(last) > 2*time.Second {
				logf(fmt.Sprintf("  已下载 %d MB…", total/(1<<20)))
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	logf(fmt.Sprintf("  下载完成（%d MB）", total/(1<<20)))
	return nil
}

// runElevated 提权执行一个系统命令（DISM 等），输出实时转进任务日志，
// 返回退出码。模式与 runElevatedWinget 一致：Start-Process -Verb RunAs
// 触发一次 UAC，stdout/stderr 重定向到临时文件边等边 tail。
func (c *Center) runElevated(t *PluginTask, exe string, args []string) (int, error) {
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("umbraforge-elev-%d.log", time.Now().UnixNano()))
	defer os.Remove(logPath)
	defer os.Remove(logPath + ".err")

	ps := fmt.Sprintf(
		`$p = Start-Process -FilePath %s -ArgumentList %s -Verb RunAs -WindowStyle Hidden -Wait -PassThru `+
			`-RedirectStandardOutput %s -RedirectStandardError %s; exit $p.ExitCode`,
		psQuote(exe), psQuote(strings.Join(args, " ")),
		psQuote(logPath), psQuote(logPath+".err"),
	)
	cmd := procutil.Command("powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", ps)

	if startErr := cmd.Start(); startErr != nil {
		return -1, fmt.Errorf("启动提权进程失败：%v", startErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	seen := 0
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case werr := <-done:
			seen = c.drainInstallLog(t, logPath, seen)
			code := 0
			if werr != nil {
				code = exitCodeOf(werr)
			}
			if tail := readLogTail(logPath+".err", 400); strings.TrimSpace(tail) != "" {
				c.taskLog(t.Key, "stderr: "+strings.TrimSpace(tail))
			}
			return code, nil
		case <-ticker.C:
			seen = c.drainInstallLog(t, logPath, seen)
		}
	}
}
