//go:build windows

package procutil

// EnsureExecutable 在 Windows 上无执行位概念，空实现保持调用点统一。
func EnsureExecutable(path string) {}
