//go:build !windows

package procutil

import (
	"os"
)

// EnsureExecutable 保证 path 具备可执行位（幂等）。
//
// 背景：分发 zip / 源码解压 / 拷贝到 .app 的过程可能丢失 x 位。
// 此时 execve 返回 EACCES，而 Go runtime 在 macOS 上 fork 子进程后
// execve 失败会走 libc exit(253) —— 触发 atexit 链里 CoreSpotlight 的
// PowerLog flush（XPC + libdispatch），在 fork 子进程里 dispatch 队列
// 已被破坏 → SIGSEGV。崩溃报告特征：
//   "*** multi-threaded process forked ***"
//   "crashed on child side of fork pre-exec"
//   exit → __cxa_finalize_ranges → [CSPowerLog flushToPowerLog] →
//   PLClientLogger xpcSend → _dispatch_mgr_queue_push
//
// 所以在启动自带插件二进制前调用本函数，把「打包时丢了权限」这类
// 问题在运行时自愈掉，而不是依赖每个分发渠道都记得 chmod。
func EnsureExecutable(path string) {
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return
	}
	if fi.Mode()&0o111 == 0 {
		_ = os.Chmod(path, 0o755)
	}
}
