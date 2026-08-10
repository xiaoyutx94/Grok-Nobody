//go:build !darwin

package plugins

// 非 macOS 平台不需要窗口保姆：
//   - Linux：打码器跑在 Xvfb 虚拟屏里，宿主桌面本来就看不到窗口。
//   - Windows：Chrome 认 --window-position/--window-size 命令行旗标，
//     由 chrome_offscreen.go 的离屏偏移直接解决。

func windowNannySupported() bool { return false }

func stashWindow(int) {}
