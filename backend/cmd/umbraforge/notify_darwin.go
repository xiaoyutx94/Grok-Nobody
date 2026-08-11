//go:build darwin

package main

import "os/exec"

// notifyAlreadyRunning 单实例冲突时给用户一个可见提示（系统对话框）。
// 失败静默忽略——提示不是关键路径。
func notifyAlreadyRunning() {
	_ = exec.Command("osascript", "-e",
		`display dialog "Grok-Nobody 已在运行（单实例模式）" buttons {"好"} default button "好" with title "Grok-Nobody"`).Run()
}
