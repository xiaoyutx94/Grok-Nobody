package plugins

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestFindSolverChromePIDsIgnoresUnrelatedProcesses(t *testing.T) {
	// 纯逻辑校验：命令行特征匹配规则
	cases := []struct {
		line string
		want bool
		why  string
	}{
		{"1234 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --user-data-dir=/tmp/ezsolver-profiles/w0", true, "ezsolver 打码实例"},
		{"1235 /usr/bin/chromium --user-data-dir=/tmp/veloraturn-profiles/a", true, "veloraturn 打码实例"},
		{"1236 /usr/bin/chromium --user-data-dir=/tmp/auralith-profiles/x", true, "auralith 打码实例"},
		{"1237 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome", false, "用户自己的 Chrome，必须放过"},
		{"1238 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --user-data-dir=/Users/me/Library/Application Support/Google/Chrome", false, "用户默认 profile"},
		{"1239 Google Chrome Helper --type=renderer --user-data-dir=/tmp/ezsolver-profiles/w0", false, "渲染子进程不是窗口进程"},
		{"1240 /usr/bin/firefox --user-data-dir=/tmp/ezsolver-profiles/w0", false, "非 Chrome"},
	}
	for _, c := range cases {
		line := strings.TrimSpace(c.line)
		isRenderer := strings.Contains(line, "--type=")
		isChrome := strings.Contains(line, "chrome") || strings.Contains(line, "Chrome") ||
			strings.Contains(line, "chromium") || strings.Contains(line, "Chromium")
		matched := false
		for _, m := range solverProfileMarkers {
			if strings.Contains(line, m) {
				matched = true
				break
			}
		}
		got := !isRenderer && isChrome && matched
		if got != c.want {
			t.Errorf("%s: got %v want %v (%s)", c.why, got, c.want, line)
		}
	}
}

func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{"123": 123, "0": 0, "": 0, "12a": 0, "-5": 0, "99999": 99999}
	for in, want := range cases {
		if got := atoiSafe(in); got != want {
			t.Errorf("atoiSafe(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestWindowNannyStartStopIdempotent(t *testing.T) {
	t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "1")
	n := NewWindowNanny()
	n.Start()
	n.Start() // 重复启动不应 panic / 不应起第二个 goroutine
	n.Stop()
	n.Stop() // 重复停止不应 panic（close 已关闭的 channel 会 panic）
}

func TestWindowNannyDisabledByEnv(t *testing.T) {
	t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "0")
	n := NewWindowNanny()
	n.Start()
	n.mu.Lock()
	running := n.running
	n.mu.Unlock()
	if running {
		t.Error("显式关闭时不应启动保姆")
	}
	n.Stop()
}

func TestWindowNannyForgetsStalePIDs(t *testing.T) {
	n := NewWindowNanny()
	n.handled[111] = time.Now()
	n.handled[222] = time.Now()
	n.forgetStale(map[int]struct{}{222: {}})
	if _, ok := n.handled[111]; ok {
		t.Error("已退出的 PID 应被清理，否则 map 无限增长")
	}
	if _, ok := n.handled[222]; !ok {
		t.Error("仍存活的 PID 不应被清理")
	}
	n.forgetStale(nil)
	if len(n.handled) != 0 {
		t.Errorf("全部退出后应清空，剩 %d", len(n.handled))
	}
}

// TestFindSolverChromePIDsAgainstRealProcess 起一个带打码 profile 特征的真实进程，
// 确认 ps 扫描能认出它，且不会把同时存在的普通进程算进来。
func TestFindSolverChromePIDsAgainstRealProcess(t *testing.T) {
	// 用 sleep 伪装：命令行里带 chrome + ezsolver-profiles 特征即可被匹配
	fake := exec.Command("/bin/sh", "-c",
		"exec -a 'Google Chrome --user-data-dir=/tmp/ezsolver-profiles/w0' sleep 30")
	if err := fake.Start(); err != nil {
		t.Skipf("无法起测试进程: %v", err)
	}
	defer func() {
		_ = fake.Process.Kill()
		_, _ = fake.Process.Wait()
	}()
	// 同时起一个不该被匹配的
	other := exec.Command("/bin/sh", "-c", "exec -a 'Google Chrome' sleep 30")
	if err := other.Start(); err == nil {
		defer func() {
			_ = other.Process.Kill()
			_, _ = other.Process.Wait()
		}()
	}
	time.Sleep(600 * time.Millisecond)

	pids := findSolverChromePIDs()
	foundFake := false
	for _, p := range pids {
		if p == fake.Process.Pid {
			foundFake = true
		}
		if other.Process != nil && p == other.Process.Pid {
			t.Errorf("误匹配了无打码特征的进程 pid=%d", p)
		}
	}
	if !foundFake {
		t.Errorf("未认出打码进程 pid=%d，实际扫到 %v", fake.Process.Pid, pids)
	}
}

func TestWindowNannyAdaptiveInterval(t *testing.T) {
	n := NewWindowNanny()
	// 从未见过打码窗口 → 慢查
	if got := n.desiredInterval(); got != nannyIdleInterval {
		t.Errorf("空闲间隔 = %v, want %v", got, nannyIdleInterval)
	}
	// 刚看到 → 快查
	n.mu.Lock()
	n.lastSeen = time.Now()
	n.mu.Unlock()
	if got := n.desiredInterval(); got != nannyActiveInterval {
		t.Errorf("活跃间隔 = %v, want %v", got, nannyActiveInterval)
	}
	// 超过驻留期 → 回落慢查
	n.mu.Lock()
	n.lastSeen = time.Now().Add(-nannyActiveLinger - time.Second)
	n.mu.Unlock()
	if got := n.desiredInterval(); got != nannyIdleInterval {
		t.Errorf("驻留期后 = %v, want %v", got, nannyIdleInterval)
	}
	if nannyActiveInterval >= nannyIdleInterval {
		t.Error("活跃间隔必须短于空闲间隔")
	}
}

func TestSolverChromeLineClassification(t *testing.T) {
	cases := map[string]bool{
		"123 Google Chrome --user-data-dir=/tmp/veloraturn-profiles/x":                      true,
		"124 /usr/bin/chromium --user-data-dir=/tmp/ezsolver-profiles/y":                    true,
		"125 Google Chrome Helper --type=renderer --user-data-dir=/tmp/ezsolver-profiles/y": false,
		"126 Google Chrome": false,
		"127 sleep 30":      false,
		"128 /usr/bin/firefox --user-data-dir=/tmp/ezsolver-profiles/y": false,
	}
	for line, want := range cases {
		if got := solverChromeLine(line); got != want {
			t.Errorf("solverChromeLine(%q) = %v, want %v", line, got, want)
		}
	}
}
