//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// AcquireLock 单实例锁（Windows）：用 LockFileEx 独占锁文件。
// 语义与 unix 版一致：已存在运行实例时返回错误（第二实例应立即退出），
// 防止双实例并发写 settings.json 互相覆盖（账号/密钥丢失）。
func AcquireLock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".instance.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	// 锁整个文件，非阻塞：已有实例持锁时立刻失败
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, fmt.Errorf("已有实例在运行（lock: %v）", err)
	}
	_ = os.WriteFile(filepath.Join(dir, ".instance.lock"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
		_ = f.Close()
	}, nil
}
