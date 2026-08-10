package plugins

// 打码浏览器「窗口保姆」：把本机打码器拉起的 Chrome 缩小并推到屏幕角落，
// 同时把焦点还给原来的前台应用，这样打码时不再挡住桌面、也不打断输入。
//
// 为什么不是隐藏 / 最小化 / headless（三条都实测否决）：
//   - 隐藏(Cmd+H)/最小化：AppKit 会把窗口 unmap，页面 visibilityState 变
//     hidden，requestAnimationFrame 完全停摆、定时器掉到 1~7%。
//     加 --disable-background-timer-throttling / --disable-renderer-backgrounding /
//     --disable-features=CalculateNativeWinOcclusion 都救不回来（实测 0% 渲染）。
//     打码器软超时 28~55s，节流后根本解不出来。
//   - headless=new：UA 带 HeadlessChrome、screen 报 800x600，
//     Cloudflare 明确把 headless 列为拦截对象。
//   - --window-position/--window-size 命令行旗标：macOS 直接忽略。
//
// 可行的是「保持窗口 mapped，但缩到最小并推出屏幕边缘」：
// 实测屏内只剩 20x111 ≈ 2220 px²（原窗口 200000 px²），
// 而定时器 100%、渲染 99%，visibilityState 仍为 visible —— 不触发任何节流。

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

// solverProfileMarkers 打码器 Chrome 的识别特征（命令行里的 user-data-dir）。
// 只认这些，绝不碰用户自己开的 Chrome。
var solverProfileMarkers = []string{
	"ezsolver-profiles",
	"veloraturn-profiles",
	"auralith-profiles",
	"turbostile-profiles",
}

// 巡检节奏自适应：一次扫描要 fork 一个 pgrep（实测 ~29ms），常驻高频会白吃 CPU。
// 打码是成批发生的，所以空闲时慢查、发现打码窗口后进入一段快查窗口期，
// 既能把窗口尽快挪走，又不会在没打码时持续占用。
// 一次扫描 fork 一个 pgrep（实测 ~29ms）。空闲 1s 一次 ≈ 单核 3%
// （12 核机器上约 0.24% 总 CPU，可忽略），保证批次里第一个窗口最多闪 1s；
// 看到打码窗口后切到 300ms，后续窗口几乎瞬间被挪走。
const (
	nannyIdleInterval   = 1 * time.Second
	nannyActiveInterval = 300 * time.Millisecond
	// 最后一次看到打码窗口后，继续保持快查多久（打码是成批的）
	nannyActiveLinger = 45 * time.Second
)

// WindowNanny 周期性地把打码 Chrome 窗口挪走。
type WindowNanny struct {
	mu       sync.Mutex
	handled  map[int]time.Time // 已处理过的 PID（避免反复挪动/夺焦）
	stop     chan struct{}
	running  bool
	interval time.Duration
	lastSeen time.Time // 最近一次看到打码 Chrome
}

// NewWindowNanny 创建保姆（未启动）。
func NewWindowNanny() *WindowNanny {
	return &WindowNanny{
		handled:  map[int]time.Time{},
		interval: nannyIdleInterval,
	}
}

// Start 启动后台巡检。重复调用无副作用。
func (n *WindowNanny) Start() {
	if !windowNannySupported() || !offscreenEnabled() {
		return
	}
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.stop = make(chan struct{})
	stop := n.stop
	n.mu.Unlock()

	go func() {
		cur := nannyIdleInterval
		timer := time.NewTimer(cur)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				n.sweepOnce()
				if next := n.desiredInterval(); next != cur {
					cur = next
				}
				timer.Reset(cur)
			}
		}
	}()
}

// Stop 停止巡检。
func (n *WindowNanny) Stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.running {
		return
	}
	n.running = false
	close(n.stop)
}

// desiredInterval 返回当前应采用的巡检间隔（见 nanny*Interval 注释）。
func (n *WindowNanny) desiredInterval() time.Duration {
	n.mu.Lock()
	last := n.lastSeen
	n.mu.Unlock()
	if !last.IsZero() && time.Since(last) < nannyActiveLinger {
		return nannyActiveInterval
	}
	return nannyIdleInterval
}

// sweepOnce 找出新出现的打码 Chrome 并挪走。
func (n *WindowNanny) sweepOnce() {
	pids := findSolverChromePIDs()
	if len(pids) == 0 {
		n.forgetStale(nil)
		return
	}
	n.mu.Lock()
	n.lastSeen = time.Now()
	n.mu.Unlock()
	live := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		live[pid] = struct{}{}
	}
	n.forgetStale(live)

	for _, pid := range pids {
		n.mu.Lock()
		_, done := n.handled[pid]
		if !done {
			n.handled[pid] = time.Now()
		}
		n.mu.Unlock()
		if done {
			continue
		}
		stashWindow(pid)
	}
}

// forgetStale 清掉已退出进程的记录，避免 map 无限增长。
func (n *WindowNanny) forgetStale(live map[int]struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for pid := range n.handled {
		if _, ok := live[pid]; !ok {
			delete(n.handled, pid)
		}
	}
}

// solverPgrepPattern 只匹配「带打码 profile 特征」的进程，交给 pgrep 在
// 系统侧过滤，避免每轮都把全部进程命令行读进来解析（实测 ps 全量 ~44ms/次，
// 按 400ms 巡检等于常驻吃掉一成 CPU）。
var solverPgrepPattern = strings.Join(solverProfileMarkers, "|")

// findSolverChromePIDs 返回打码器拉起的顶层 Chrome 进程 PID。
// 用 user-data-dir 特征匹配，排除渲染进程（--type=）与用户自己的 Chrome。
func findSolverChromePIDs() []int {
	// -f 全命令行匹配，-d 输出分隔符；无匹配时 pgrep 退出码 1，属正常情况。
	out, err := exec.Command("pgrep", "-f", "-l", solverPgrepPattern).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !solverChromeLine(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if pid := atoiSafe(fields[0]); pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// solverChromeLine 判定一行 "pid command..." 是否是打码器的顶层 Chrome 窗口进程。
func solverChromeLine(line string) bool {
	// 渲染/GPU 子进程不是顶层窗口进程
	if strings.Contains(line, "--type=") {
		return false
	}
	low := strings.ToLower(line)
	if !strings.Contains(low, "chrome") && !strings.Contains(low, "chromium") {
		return false
	}
	for _, marker := range solverProfileMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
