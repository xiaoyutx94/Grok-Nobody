package procutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 回归：插件二进制丢了 x 位时，exec 会 EACCES；而 Go 在 macOS 上 fork 出的
// 子进程 exec 失败会走 libc exit(253) → atexit 链里 CoreSpotlight 的
// PowerLog flush → 在已被 fork 破坏的 dispatch 队列上操作 → SIGSEGV。
// 崩溃报告特征：multi-threaded process forked / crashed on child side of
// fork pre-exec。所以启动前必须把 x 位补回来，而不是依赖打包脚本记得 chmod。
func TestEnsureExecutableRestoresMissingExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无执行位概念")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "auralithd-darwin-arm64")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 != 0 {
		t.Fatal("前置条件失败：文件本应没有 x 位")
	}

	EnsureExecutable(bin)

	fi, err = os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("x 位没补上，mode=%v —— exec 仍会 EACCES 触发 fork-child 崩溃", fi.Mode())
	}
}

// 已有 x 位时不应改动权限（避免把 0o750 拉宽成 0o755）。
func TestEnsureExecutableLeavesExecutableFileAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无执行位概念")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "already-exec")
	if err := os.WriteFile(bin, []byte("x"), 0o750); err != nil {
		t.Fatal(err)
	}
	EnsureExecutable(bin)
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Fatalf("权限被无谓改写：%v，want 0750", fi.Mode().Perm())
	}
}

// 空路径 / 不存在 / 目录都不能 panic。
func TestEnsureExecutableIsSafeOnBadInput(t *testing.T) {
	EnsureExecutable("")
	EnsureExecutable(filepath.Join(t.TempDir(), "nope"))
	EnsureExecutable(t.TempDir()) // 目录
}
