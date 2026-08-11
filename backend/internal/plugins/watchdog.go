package plugins

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 打码引擎卡死自愈 watchdog：
// 引擎「活着但空转」——浏览器/worker 全被卡死请求占满（如 auralith
// browser_busy=8 满且 solved 长时间不增长，新请求全部 503 high-water
// exceeded），注册就会 0 成功。此 watchdog 周期性探测三个内置引擎的
// health，判定卡死 → 自动 Stop + Install 重启（清理进程与浏览器池），
// 用户无需手动干预。

const (
	watchdogInterval      = 15 * time.Second
	watchdogStallPeriods  = 3 // 连续 3 个周期（45s）solved 无增长 = 卡死
	watchdogRestartCoold  = 10 * time.Minute
	watchdogHealthTimeout = 4 * time.Second
)

type engineSnapshot struct {
	Solved int64
	Active int
	Busy   int
	Seen   bool
}

type engineWatchState struct {
	last     engineSnapshot
	stallCnt int
	lastRest time.Time
}

// startEngineWatchdog 启动引擎自愈监视（app 启动后调用一次）。
// 公开入口：cmd 层延迟 20s 调用，避开引擎启动窗口。
func (c *Center) StartEngineWatchdog() {
	go func() {
		states := make(map[PluginID]*engineWatchState)
		tick := time.NewTicker(watchdogInterval)
		defer tick.Stop()
		for range tick.C {
			for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
				st := c.Status(id)
				if !st.Installed || !st.Running {
					continue
				}
				snap := probeEngineHealth(captchaPort(id))
				if !snap.Seen {
					continue // 探测失败（超时/无响应）由既有健康检查覆盖，不武断重启
				}
				ws, ok := states[id]
				if !ok {
					states[id] = &engineWatchState{last: snap}
					continue
				}
				stalled := snap.Solved == ws.last.Solved && snap.Active > 0
				if stalled {
					ws.stallCnt++
					// busy 满 + 无出码 = 典型卡死（新请求 503 high-water）
					if ws.stallCnt >= watchdogStallPeriods && time.Since(ws.lastRest) > watchdogRestartCoold {
						log.Printf("[自愈] %s 疑似卡死（%d 周期无出码，active=%d busy=%d solved=%d）→ 自动重启引擎",
							id, ws.stallCnt, snap.Active, snap.Busy, snap.Solved)
						ws.lastRest = time.Now()
						ws.stallCnt = 0
						c.restartEngine(id)
						states[id] = &engineWatchState{last: engineSnapshot{}, lastRest: ws.lastRest}
						continue
					}
				} else {
					ws.stallCnt = 0
				}
				ws.last = snap
			}
		}
	}()
}

// restartEngine 重启引擎：Stop（杀进程/清端口/停容器）→ Install（同模式拉起）。
func (c *Center) restartEngine(id PluginID) {
	mode := c.mode(id)
	if _, err := c.Stop(id); err != nil {
		log.Printf("[自愈] %s Stop 失败: %v", id, err)
		return
	}
	time.Sleep(1 * time.Second)
	if _, err := c.Install(InstallRequest{ID: id, Mode: mode}); err != nil {
		log.Printf("[自愈] %s 重启失败: %v（请到打码设置手动处理）", id, err)
		return
	}
	log.Printf("[自愈] %s 已自动重启（mode=%s），浏览器/进程池已清理", id, mode)
}

// probeEngineHealth 读取引擎 /health 的关键指标（solved/active/busy）。
// 字段名跨引擎兼容：solved|total_solved|solved_count / active|active_requests /
// busy|browser_busy|workers_busy。
func probeEngineHealth(port int) engineSnapshot {
	client := &http.Client{Timeout: watchdogHealthTimeout}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		return engineSnapshot{}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return engineSnapshot{}
	}
	snap := engineSnapshot{Seen: true}
	snap.Solved = firstInt(m, "solved", "total_solved", "solved_count", "tasks_solved")
	snap.Active = int(firstInt(m, "active", "active_requests", "running"))
	snap.Busy = int(firstInt(m, "busy", "browser_busy", "workers_busy", "active_contexts"))
	return snap
}

func firstInt(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case string:
			var n int64
			if err := json.Unmarshal([]byte(strings.TrimSpace(v)), &n); err == nil {
				return n
			}
		}
	}
	return 0
}
