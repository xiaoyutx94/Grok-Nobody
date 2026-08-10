//go:build darwin && live

package plugins

// 实机验证（需要真实 Chrome + 桌面会话）：
//   go test ./internal/plugins/ -tags live -run TestWindowNannyLive -v
//
// 起两个真实 Chrome：一个 user-data-dir 带打码特征、一个模拟用户自己的浏览器。
// 跑一次 sweep，断言只有打码那个被缩小挪走，用户那个原位不动，且页面未被节流。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const liveChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

func liveOsa(script string) string {
	out, _ := exec.Command("osascript", "-e", script).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func liveGeom(pid int) string {
	return liveOsa(fmt.Sprintf(
		`tell application "System Events" to tell (first process whose unix id is %d) to get {position, size} of window 1`, pid))
}

func liveLaunch(t *testing.T, port int, profile, tag string) *exec.Cmd {
	t.Helper()
	_ = os.RemoveAll(profile)
	cmd := exec.Command(liveChrome,
		"--remote-debugging-port="+strconv.Itoa(port),
		"--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--remote-allow-origins=*", "--no-sandbox",
		"--disable-features=CalculateNativeWinOcclusion",
		"--disable-backgrounding-occluded-windows",
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"data:text/html,<body><h1>"+tag+"</h1></body>")
	if err := cmd.Start(); err != nil {
		t.Fatalf("launch %s: %v", tag, err)
	}
	for i := 0; i < 80; i++ {
		out, err := exec.Command("curl", "-s", "-m", "1",
			fmt.Sprintf("http://127.0.0.1:%d/json/version", port)).Output()
		if err == nil && len(out) > 10 {
			return cmd
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s CDP 未就绪", tag)
	return cmd
}

func liveMeasure(t *testing.T, port int) map[string]any {
	t.Helper()
	script := `
import json,sys,urllib.request,websocket
port=int(sys.argv[1])
js=("(async()=>{let n=0,f=0;const t0=performance.now();"
    "const iv=setInterval(()=>n++,4);"
    "const raf=()=>{f++;if(performance.now()-t0<800)requestAnimationFrame(raf);};"
    "requestAnimationFrame(raf);"
    "await new Promise(r=>setTimeout(r,800));clearInterval(iv);"
    "return JSON.stringify({ticks:n,frames:f,vis:document.visibilityState});})()")
t=json.loads(urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list",timeout=3).read())
page=next(x for x in t if x.get("type")=="page")
ws=websocket.create_connection(page["webSocketDebuggerUrl"],timeout=10,origin="",suppress_origin=True)
ws.send(json.dumps({"id":1,"method":"Runtime.evaluate",
                    "params":{"expression":js,"awaitPromise":True,"returnByValue":True}}))
while True:
    g=json.loads(ws.recv())
    if g.get("id")==1:
        print(g["result"]["result"]["value"]); break
ws.close()
`
	out, err := exec.Command("python3", "-c", script, strconv.Itoa(port)).Output()
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &m); err != nil {
		t.Fatalf("measure parse %q: %v", out, err)
	}
	return m
}

func parse4(s string) []int {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return nil
	}
	out := make([]int, 0, 4)
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func TestWindowNannyLive(t *testing.T) {
	if _, err := os.Stat(liveChrome); err != nil {
		t.Skip("no Chrome")
	}
	t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "1")

	solverProfile := "/tmp/ezsolver-profiles/live-w0"
	userProfile := "/tmp/uf-live-user"

	solver := liveLaunch(t, 9931, solverProfile, "SOLVER")
	user := liveLaunch(t, 9932, userProfile, "USER")
	t.Cleanup(func() {
		for _, c := range []*exec.Cmd{solver, user} {
			if c.Process != nil {
				_ = c.Process.Kill()
				_, _ = c.Process.Wait()
			}
		}
		_ = os.RemoveAll(solverProfile)
		_ = os.RemoveAll(userProfile)
	})
	time.Sleep(1500 * time.Millisecond)

	sPid, uPid := solver.Process.Pid, user.Process.Pid
	beforeUser := liveGeom(uPid)
	t.Logf("挪动前 打码=%s 用户=%s", liveGeom(sPid), beforeUser)
	t.Logf("挪动前 打码性能=%v", liveMeasure(t, 9931))

	// 确认扫描能认出打码实例
	pids := findSolverChromePIDs()
	found := false
	for _, p := range pids {
		if p == sPid {
			found = true
		}
		if p == uPid {
			t.Errorf("误把用户 Chrome (pid=%d) 认成打码实例", uPid)
		}
	}
	if !found {
		t.Fatalf("未认出打码 Chrome pid=%d，扫到 %v", sPid, pids)
	}

	n := NewWindowNanny()
	n.sweepOnce()
	time.Sleep(2500 * time.Millisecond)

	afterSolver := liveGeom(sPid)
	afterUser := liveGeom(uPid)
	t.Logf("挪动后 打码=%s 用户=%s", afterSolver, afterUser)

	screenW, screenH := screenSize()
	if g := parse4(afterSolver); len(g) == 4 {
		L, T, W, H := g[0], g[1], g[2], g[3]
		ix := max(0, min(L+W, screenW)-max(L, 0))
		iy := max(0, min(T+H, screenH)-max(T, 0))
		area := ix * iy
		t.Logf("打码窗口屏内残留 %dx%d = %d px²（窗口 %dx%d = %d px²）", ix, iy, area, W, H, W*H)
		if area > 20000 {
			t.Errorf("打码窗口仍占据 %d px² 可见区域，未挪出视线", area)
		}
	} else {
		t.Errorf("读不到打码窗口几何: %q", afterSolver)
	}

	if afterUser != beforeUser {
		t.Errorf("用户 Chrome 被动了: %q -> %q", beforeUser, afterUser)
	}

	m := liveMeasure(t, 9931)
	if m["vis"] != "visible" {
		t.Errorf("打码页面被判 %v，会被节流", m["vis"])
	}
	if ticks, ok := m["ticks"].(float64); ok && ticks < 100 {
		t.Errorf("挪动后定时器掉到 %v ticks/800ms，说明被节流", ticks)
	}
	t.Logf("挪动后 打码性能=%v", m)
}
