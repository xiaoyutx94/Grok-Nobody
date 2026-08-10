package plugins

// 打码浏览器移出可见桌面。
//
// 三个免费打码器（EzSolver / VeloraTurn / Auralith）都必须用 headful Chrome：
// headless 会让 navigator.webdriver 与 site-isolation 特征暴露，Turnstile 直接
// 判定自动化（solver.go/solver.py 的注释里有实测记录）。Linux 上它们跑在 Xvfb
// 虚拟屏里所以看不见，但 macOS / Windows 没有 Xvfb，于是每次打码都往桌面弹窗。
//
// 做法：不动 headless，改用 --window-position 把窗口挪到屏幕外。
// EzSolver / VeloraTurn 已支持 EZSOLVER_OFFSCREEN 自己加旗标；Auralith 是预编译
// 二进制无法改，但它认 CHROME_PATH，所以这里生成一个包装脚本，由包装脚本在
// 真实 Chrome 的参数后追加离屏旗标。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// offscreenPosition 远离任何真实显示器的坐标。
const offscreenPosition = "-32000,-32000"

// offscreenEnabled 报告是否需要离屏。默认非 Linux 开启（Linux 有 Xvfb）。
// UMBRAFORGE_CAPTCHA_OFFSCREEN=0 可显式关掉（调试打码时想看窗口）。
func offscreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UMBRAFORGE_CAPTCHA_OFFSCREEN"))) {
	case "0", "false", "no":
		return false
	case "1", "true", "yes":
		return true
	}
	return runtime.GOOS != "linux"
}

// findChromeBinary 定位真实 Chrome/Chromium。优先已有 CHROME_PATH。
func findChromeBinary() string {
	if p := strings.TrimSpace(os.Getenv("CHROME_PATH")); p != "" {
		if fileExists(p) {
			return p
		}
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	case "windows":
		cands = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
	default:
		cands = []string{
			"/snap/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
		}
	}
	for _, p := range cands {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// ensureChromeOffscreenWrapper 生成（或刷新）包装脚本并返回其路径。
// 脚本把离屏旗标追加到调用方给的参数后面，再 exec 真实 Chrome。
// 返回空字符串表示不需要或无法包装，调用方应保持原样。
func (c *Center) ensureChromeOffscreenWrapper() string {
	if !offscreenEnabled() {
		return ""
	}
	if runtime.GOOS == "windows" {
		// Windows 上 CHROME_PATH 需要指向 .exe，shell 包装不通用；
		// EzSolver / VeloraTurn 已能自己加旗标，Auralith 在 Windows 上保持原样。
		return ""
	}
	chrome := findChromeBinary()
	if chrome == "" {
		return ""
	}
	dir := filepath.Join(c.root, "plugins", ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, "chrome-offscreen.sh")
	// exec 保证信号与退出码直通，打码器的进程管理（kill/reap）行为不变。
	// "$@" 在前、离屏旗标在后：Chrome 后出现的同名旗标生效，
	// 因此即使调用方自己传了 window-position 也会被这里覆盖成离屏。
	script := fmt.Sprintf(`#!/bin/sh
# 由 UmbraForge 自动生成：让打码 Chrome 在屏幕外运行（headful 但不可见）。
# headless 会破坏 Turnstile 通过率，所以这里只挪窗口位置。
exec %s "$@" --window-position=%s --window-size=1280,800
`, shellQuote(chrome), offscreenPosition)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return ""
	}
	return path
}

// shellQuote 单引号包裹并转义内部单引号（Chrome 路径含空格，如 macOS 的
// "Google Chrome.app"，不引会被拆成两个参数）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// applyCaptchaOffscreenEnv 给打码器进程注入离屏相关环境变量。
// 返回追加后的 env。env 为 nil 时以当前进程环境为基底。
func (c *Center) applyCaptchaOffscreenEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	if !offscreenEnabled() {
		// 显式关闭时把开关透传给支持自加旗标的打码器，避免它们按平台默认又开上
		return upsertEnv(env, "EZSOLVER_OFFSCREEN", "0")
	}
	env = upsertEnv(env, "EZSOLVER_OFFSCREEN", "1")
	if wrapper := c.ensureChromeOffscreenWrapper(); wrapper != "" {
		// Auralith 只认 CHROME_PATH，包装脚本负责补旗标
		env = upsertEnv(env, "CHROME_PATH", wrapper)
	} else if chrome := findChromeBinary(); chrome != "" {
		// Windows：不生成 shell 包装（对 .exe 不通用），但必须把真实 Chrome
		// 路径交给 VeloraTurn / Auralith —— 它们不认 CHROME_PATH 就找不到
		// 浏览器，打码全部失败（实测日志：WARNING: no chrome found (set
		// CHROME_PATH); solves will fail）。离屏旗标由各引擎自己加
		// （EZSOLVER_OFFSCREEN 透传 + 各自实现）。
		env = upsertEnv(env, "CHROME_PATH", chrome)
	}
	return env
}

// upsertEnv 设置或替换 KEY=VALUE（避免同名变量重复，取值以最后一个为准的行为不可靠）。
func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}
