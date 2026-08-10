//go:build !windows

package procutil

import "os/exec"

// Hide 在非 Windows 平台是空操作：macOS/Linux 不存在「子进程自动获得控制台窗口」
// 这回事，SysProcAttr 上也没有 HideWindow 字段（编译期就不通过）。
//
// 保留同名空实现是为了让调用方无需自己写 build tag —— 全项目统一用
// procutil.Command 即可，跨平台行为差异收敛在这个包里。
func Hide(cmd *exec.Cmd) { _ = cmd }
