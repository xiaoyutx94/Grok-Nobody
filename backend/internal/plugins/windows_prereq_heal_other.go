//go:build !windows

package plugins

import "fmt"

// 非 Windows 平台桩实现：macOS 走 colima、Linux 走原生 Docker，
// 没有「启用虚拟机平台 / 装 Python」这类 Windows 专属前置。

// healWindowsPrereqs 仅 Windows 有意义，其它平台恒为 false（无需重启）。
func (c *Center) healWindowsPrereqs(t *PluginTask) bool { return false }

// ensureWindowsPython 仅 Windows 有意义，其它平台由系统包管理器提供。
func ensureWindowsPython(logPath string) (string, error) {
	return "", fmt.Errorf("仅 Windows 支持自动安装 Python")
}
