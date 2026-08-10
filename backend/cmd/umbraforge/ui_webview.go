//go:build !nogui

package main

import (
	"runtime"

	webview "github.com/webview/webview_go"
)

// runUI 启动原生 webview 桌面窗口(headful)。
// 用 -tags nogui 构建可跳过本文件,得到无 GUI 依赖的服务版(见 ui_nogui.go)。
func runUI(ui string) {
	runtime.LockOSThread()
	// Install Cocoa Edit menu so Cmd+C/V/X/A reach WKWebView inputs.
	installNativeEditMenu()
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Grok-Nobody")
	w.SetSize(1440, 920, webview.HintNone)
	applyWindowIcon(w)
	// Clipboard: native Cocoa Edit menu only (no JS paste — avoids double insert).
	w.Navigate(ui)
	w.Run()
}
