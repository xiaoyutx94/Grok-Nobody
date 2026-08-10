//go:build windows

package main

// Windows 专属：把嵌入的 app.ico 设为窗口/任务栏图标。
// exe 文件图标由 winres 生成的 rsrc_windows_amd64.syso 提供（资源管理器
// 显示），这里负责运行时窗口标题栏 + 任务栏的图标（webview 默认无图标）。

import (
	_ "embed"
	"unsafe"

	"github.com/webview/webview_go"
	"golang.org/x/sys/windows"
)

//go:embed app.ico
var appIconICO []byte

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procSendMessageW           = user32.NewProc("SendMessageW")
	procCreateIconFromResource = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
)

const (
	wmSetIcon   = 0x0080
	iconBig     = 1 // ICON_BIG
	iconSmall   = 0 // ICON_SMALL
	iconSmall2  = 2 // ICON_SMALL2 (WinXP+ 任务栏)
	lrDefault   = 0x0000
	langNeutral = 0
)

// applyWindowIcon 设置窗口标题栏与任务栏图标（从嵌入的 ICO 资源创建 HICON）。
func applyWindowIcon(w webview.WebView) {
	hwnd := w.Window()
	if hwnd == nil || len(appIconICO) == 0 {
		return
	}
	hicon, _, _ := procCreateIconFromResource.Call(
		uintptr(unsafe.Pointer(&appIconICO[0])),
		uintptr(len(appIconICO)),
		1,          // 真彩色 ICO（非 cursor）
		0x00030000, // VERSION 3.0
		0, 0,
		uintptr(lrDefault),
	)
	if hicon == 0 {
		return
	}
	defer procDestroyIcon.Call(hicon)
	for _, which := range []uintptr{iconBig, iconSmall, iconSmall2} {
		procSendMessageW.Call(uintptr(hwnd), wmSetIcon, which, hicon)
	}
}
