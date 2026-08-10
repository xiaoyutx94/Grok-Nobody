//go:build !windows && !nogui

package main

import "github.com/webview/webview_go"

// 非 Windows 平台：无窗口图标设置（macOS 由 .app bundle 图标提供，
// Linux 由窗口管理器/桌面文件提供）。
func applyWindowIcon(w webview.WebView) {}
