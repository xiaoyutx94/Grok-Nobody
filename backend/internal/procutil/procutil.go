// Package procutil 提供「不弹控制台窗口」的子进程构造器。
//
// 为什么需要这个包：Windows 上用 exec.Command 启动任何控制台程序，系统都会
// 为它分配一个新的 conhost 窗口。UmbraForge 有 60+ 处子进程调用
// （docker/colima/wmic/tasklist/打码引擎二进制…），其中不少是**每几秒轮询一次**
// 的（Docker 状态、容器列表、WARP 健康检查），于是桌面上会持续闪黑框；
// 打码引擎是长驻进程，它的控制台窗口会一直挂在任务栏上不走。
//
// 关键点是必须在**构造时**就设好 SysProcAttr：很多调用点写成
// exec.Command(...).Output() 这种一次性链式表达式，拿不到 *exec.Cmd 去事后修改。
// 所以这里提供与 os/exec 同名的构造器，全项目把 exec.Command 换成
// procutil.Command 即可，调用形态完全不变。
//
// 注意：这只隐藏**控制台窗口**，不影响子进程自己创建的 GUI 窗口。
// 打码引擎必须开有头浏览器（headless 会被 Turnstile 判定自动化），
// 那些 Chrome 窗口依然会显示 —— 它们由 plugins.WindowNanny 负责缩小并挪到
// 屏幕角落，与本包无关。
package procutil

import (
	"context"
	"os/exec"
)

// Command 等价于 exec.Command，但在 Windows 上不弹控制台窗口。
func Command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	Hide(cmd)
	return cmd
}

// CommandContext 等价于 exec.CommandContext，但在 Windows 上不弹控制台窗口。
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	Hide(cmd)
	return cmd
}
