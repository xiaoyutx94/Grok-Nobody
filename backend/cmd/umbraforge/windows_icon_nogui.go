//go:build nogui

package main

// applyWindowIcon 在 nogui 构建下无 webview 窗口,无需设置窗口图标。
func applyWindowIcon(_ any) {}
