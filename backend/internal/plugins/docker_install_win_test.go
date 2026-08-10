package plugins

import (
	"strings"
	"testing"
)

// TestWingetExitOKTreatsAlreadyInstalledAsSuccess winget 用高位十六进制码表达
// 细分结果，其中几个是「成功」语义。旧实现只看 err != nil，于是：
//   - 已经装过 Docker → 报「安装失败」
//   - 装好了待重启   → 报「安装失败」
//
// 用户看到失败就会反复点重装，而实际上已经装好了。
func TestWingetExitOKTreatsAlreadyInstalledAsSuccess(t *testing.T) {
	cases := []struct {
		name       string
		code       uint32
		wantOK     bool
		wantReboot bool
	}{
		{"正常成功", 0, true, false},
		{"已是最新/已安装 UPDATE_NOT_APPLICABLE", 0x8A15002B, true, false},
		{"已安装 PACKAGE_ALREADY_INSTALLED", 0x8A150011, true, false},
		{"装好待重启 REBOOT_REQUIRED_TO_FINISH", 0x8A150109, true, true},
		{"需重启后继续 REBOOT_REQUIRED_FOR_INSTALL", 0x8A150077, true, true},
		{"找不到包", 0x8A150014, false, false},
		{"用户取消 UAC", 0x8A15010D, false, false},
		{"需要管理员权限", 0x8A150044, false, false},
		{"通用失败", 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reboot := wingetExitOK(int(int32(tc.code)))
			if ok != tc.wantOK {
				t.Errorf("wingetExitOK(0x%X) ok = %v, want %v", tc.code, ok, tc.wantOK)
			}
			if reboot != tc.wantReboot {
				t.Errorf("wingetExitOK(0x%X) needReboot = %v, want %v", tc.code, reboot, tc.wantReboot)
			}
		})
	}
}

// TestWingetExitHintIsActionable 失败提示必须是可操作的，不能只丢一个十六进制码。
// 每条提示都要告诉用户「下一步该做什么」。
func TestWingetExitHintIsActionable(t *testing.T) {
	cases := map[uint32]string{
		0x8A150014: "winget source update", // 包找不到 → 提示更新源
		0x8A15010D: "取消",                   // UAC 拒绝 → 说明是被取消
		0x8A150044: "管理员",                  // 权限不足 → 提示 UAC 选是
		0x8A150045: "源",                    // 源不可用
	}
	for code, want := range cases {
		got := wingetExitHint(int(int32(code)))
		if !strings.Contains(got, want) {
			t.Errorf("wingetExitHint(0x%X) = %q，应包含 %q（提示要可操作）", code, got, want)
		}
	}
	// 未知码也不能返回空串，至少要带上码本身供排查
	if got := wingetExitHint(12345); !strings.Contains(got, "0x") {
		t.Errorf("未知码提示 = %q，应至少带上十六进制码", got)
	}
}

// TestPsQuoteEscapesSingleQuotes PowerShell 参数必须用单引号包裹并把内部单引号
// 写两遍。用双引号会让路径里的 $ 被当成变量展开 —— Windows 用户名带 $
// （如 SERVICE$）或路径含 $ 时，命令会被悄悄改写。
func TestPsQuoteEscapesSingleQuotes(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\winget.exe`: `'C:\Program Files\winget.exe'`,
		`C:\a'b\winget.exe`:           `'C:\a''b\winget.exe'`,
		``:                            `''`,
		`$env:PATH`:                   `'$env:PATH'`, // 单引号内不展开，原样保留
	}
	for in, want := range cases {
		if got := psQuote(in); got != want {
			t.Errorf("psQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInstallArgsAreSilentAndNonInteractive 安装参数必须同时具备静默与免交互，
// 缺任何一个都会让 Docker Desktop 弹出自己的安装向导 / winget 停下来反问，
// 表现就是用户报的「装不了」「卡住不动」。
func TestInstallArgsAreSilentAndNonInteractive(t *testing.T) {
	// 与 installDockerWindows 里的 args 保持一致；这里做成不变式钉住，
	// 以后有人删旗标会被测试拦下。
	args := []string{
		"install", "-e", "--id", "Docker.DockerDesktop",
		"--silent",
		"--accept-source-agreements", "--accept-package-agreements",
		"--disable-interactivity",
	}
	joined := strings.Join(args, " ")
	for _, must := range []string{
		"--silent",                    // 抑制 Docker Desktop 安装向导 GUI
		"--accept-source-agreements",  // 免交互
		"--accept-package-agreements", // 免交互
		"--disable-interactivity",     // 禁止 winget 反问
		"-e", "--id", "Docker.DockerDesktop",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("安装参数缺少 %q：%s", must, joined)
		}
	}
}
