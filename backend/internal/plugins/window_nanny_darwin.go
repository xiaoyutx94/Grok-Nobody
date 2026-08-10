//go:build darwin

package plugins

// macOS 实现：用 System Events 按 PID 缩小并推走窗口，然后把焦点还给原前台应用。

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func windowNannySupported() bool { return true }

// stashSize 缩到 macOS 允许的最小可用尺寸附近（更小会被夹回）。
const stashW, stashH = 500, 400

// stashEdgeKeep 故意留在屏内的像素（完全移出会被 macOS 夹回屏幕中央）。
const stashEdgeKeep = 20

func osaRun(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// screenSize 读主屏逻辑分辨率。
func screenSize() (int, int) {
	out, err := osaRun(`tell application "Finder" to get bounds of window of desktop`)
	if err != nil || out == "" {
		return 1920, 1080
	}
	parts := strings.Split(out, ",")
	if len(parts) != 4 {
		return 1920, 1080
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[2]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[3]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 1920, 1080
	}
	return w, h
}

// frontmostApp 返回当前前台应用名（用于挪窗后还原焦点）。
func frontmostApp() string {
	out, err := osaRun(`tell application "System Events" to get name of first process whose frontmost is true`)
	if err != nil {
		return ""
	}
	return out
}

// stashWindow 把指定 PID 的窗口缩小并推到右下角外，最后还原焦点。
func stashWindow(pid int) {
	prevFront := frontmostApp()

	w, h := screenSize()
	left := w - stashEdgeKeep
	top := h - stashEdgeKeep

	// 缩小 + 挪走一次做完，减少可见抖动。窗口保持 mapped 状态，
	// 所以 visibilityState 仍是 visible，不会触发 hidden 节流。
	script := fmt.Sprintf(`tell application "System Events"
	if exists (first process whose unix id is %d) then
		tell (first process whose unix id is %d)
			repeat with wi in windows
				try
					set size of wi to {%d, %d}
					set position of wi to {%d, %d}
				end try
			end repeat
		end tell
	end if
end tell`, pid, pid, stashW, stashH, left, top)
	_, _ = osaRun(script)

	// 焦点还给原来的应用：打码窗口出现时会抢前台，打断用户输入。
	if prevFront != "" && !strings.Contains(strings.ToLower(prevFront), "chrome") &&
		!strings.Contains(strings.ToLower(prevFront), "chromium") {
		_, _ = osaRun(fmt.Sprintf(`tell application %q to activate`, prevFront))
	}
}
