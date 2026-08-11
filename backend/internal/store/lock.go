package store

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// AcquireLock 单实例锁：在 dataDir 下 flock 一个锁文件。
// 已存在运行实例时返回错误（第二实例应立即退出）。
//
// 为什么必须：settings.json 是单文件 JSON store，双实例并发写会互相覆盖
// （账号/密钥/配置丢失）；且第二实例退出路径会触发 macOS 系统级崩溃
// （多线程进程 fork 子进程后 exit → CoreSpotlight atexit → PowerLog XPC
// → dispatch 队列损坏 SIGSEGV，macOS 27 beta）。
func AcquireLock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".instance.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("已有实例在运行（flock: %v）", err)
	}
	// 写入 PID 便于诊断
	_, _ = f.Seek(0, 0)
	_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
	_ = f.Truncate(int64(len(fmt.Sprintf("%d\n", os.Getpid()))))
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
