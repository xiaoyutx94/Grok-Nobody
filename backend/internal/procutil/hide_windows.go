//go:build windows

package procutil

import (
	"os/exec"
	"syscall"
)

// createNoWindow 对应 Win32 的 CREATE_NO_WINDOW 进程创建标志。
//
// 不用 golang.org/x/sys/windows 的常量是为了不给这个小包引入依赖；
// 值来自 Windows API 文档（processthreadsapi.h），稳定不变。
const createNoWindow = 0x08000000

// Hide 让子进程不分配控制台窗口。
//
// 两个标志都要设，缺一不可：
//   - HideWindow 只影响 STARTUPINFO.wShowWindow（要求子进程尊重它），
//     控制台程序仍会被系统分配 conhost 窗口；
//   - CreationFlags 里的 CREATE_NO_WINDOW 才是真正阻止分配控制台的那一项。
//
// 已有 SysProcAttr 时只补字段，不整体覆盖 —— 避免踩掉调用方设的其它属性。
func Hide(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
