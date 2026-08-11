//go:build darwin

package main

/*
#include <unistd.h>

// _exit 是下划线开头符号，cgo 不导出；用静态包装函数暴露。
static void umbraforge_exit_now(int code) {
	_exit(code);
}
*/
import "C"

// exitNoAtexit 直接 _exit：绕过 libc atexit 清理链。
//
// 背景：多线程进程 fork 子进程（Go exec/posix_spawn 内部）后，子进程 exit 时
// 会执行 __cxa_finalize → CoreSpotlight 的 atexit handler（flushToPowerLog）
// → PowerLog XPC 消息 → 操作已被 fork 破坏的 dispatch 队列 → SIGSEGV
// （崩溃报告特征：multi-threaded process forked / crashed on child side of
// fork pre-exec）。macOS 27 beta 上尤为常见。
// 单实例冲突/启动失败等「主动退出」路径一律走 _exit，不触发该系统崩溃。
func exitNoAtexit(code int) {
	C.umbraforge_exit_now(C.int(code))
}
