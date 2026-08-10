//go:build nogui

package main

import "log"

// runUI 在 nogui 构建(无 webview 依赖)下不创建原生窗口,
// 由用户用浏览器访问内嵌 UI。用于无 webkit2gtk 环境的交叉编译发布版。
func runUI(ui string) {
	log.Printf("nogui build: open %s in your browser (Ctrl+C to stop)", ui)
	waitSignal()
}
