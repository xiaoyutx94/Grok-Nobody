package grokregister

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RegisterResult is one account outcome from Run / RunBatch.
type RegisterResult map[string]any

// BatchProgress is reported during RunBatch.
type BatchProgress struct {
	Total     int
	Completed int
	Success   int
	Fail      int
	Running   bool
	Message   string
	LastEmail string
	LastError string
	// AutoPaused: 连续失败自动暂停是否激活（当前处于暂停等待窗口）。
	AutoPaused bool
	// AutoPauseRemainingSec: 暂停窗口剩余秒数（0 表示不在暂停）。
	AutoPauseRemainingSec int
	// Proxy traffic for this batch (approx body bytes via proxy).
	ProxyTxBytes  int64
	ProxyRxBytes  int64
	ProxyRequests int64
}

// BatchOptions configures a local Grok register batch (1:1 with Kiro runGrokBatch knobs).
type BatchOptions struct {
	Count       int
	Concurrency int
	DelaySec    int
	StepDelay   int
	SkipVerify  bool
	Browser     string
	OSType      string
	EmailMode   string // mailtm | outlook | cfworker | managed_temp | edu
	// EmailServices pre-built pool (outlook/cfworker/managed). mailtm creates per-task.
	EmailServices []EmailService
	ProxyPool     []string // proxy URLs; empty = direct
	// CaptchaProxyMode controls only the proxy forwarded to the free captcha
	// solver. Registration HTTP continues to use ProxyPool independently.
	CaptchaProxyMode string
	// CaptchaProxyPool is used by global/selected modes. Global rotates this pool
	// by worker; selected contains exactly one fixed proxy.
	CaptchaProxyPool []string
	// SkipProxyPreCheck disables PreCheckProxyPool (tests / caller already checked).
	SkipProxyPreCheck bool
	Captcha           CaptchaOptions
	BatchConfig       *BatchConfig
	// OnProgress optional callback (may be called from workers).
	OnProgress func(BatchProgress)
	// OnSuccess called when one account succeeds (for immediate import).
	// Prefer keeping this non-blocking; long import should be async in the caller.
	OnSuccess func(RegisterResult)
	// OnResult called for every finished job (success or fail), after counters update.
	OnResult func(RegisterResult)
	// OnOTPVerified called right after the email code passes verification
	// (before submit). iCloud 模式用它及时删除用过的 HME（用完即删，
	// 避免收件箱堆积/配额占用）。调用方须保证非阻塞。
	OnOTPVerified func(email string)
	// CFPickMode: ""|"round_robin" (default) | "random" — how to pick among CFWorker EmailServices.
	CFPickMode string
	// AutoPauseOnFailures: 连续失败达到 AutoPauseFailThreshold 次后暂停
	// AutoPauseWaitMinutes 分钟再继续，无限循环（上游风控窗口自动避让）。
	AutoPauseOnFailures    bool
	AutoPauseWaitMinutes   int
	AutoPauseFailThreshold int
}

// autoPauseTracker 实现「连续失败自动暂停」状态机：
// 连续失败达到阈值 → 暂停 wait 时长 → 自动继续；任何成功清零连续失败计数。
// 多 worker 共享同一个 tracker（连续失败按整体注册看，不是单 worker）。
type autoPauseTracker struct {
	mu         sync.Mutex
	streak     int
	pauseUntil time.Time
	threshold  int
	wait       time.Duration
}

func newAutoPauseTracker(threshold int, waitMinutes int) *autoPauseTracker {
	if threshold <= 0 {
		threshold = 10
	}
	if waitMinutes <= 0 {
		waitMinutes = 5
	}
	return &autoPauseTracker{threshold: threshold, wait: time.Duration(waitMinutes) * time.Minute}
}

// recordResult 记录一次注册结果（ok=成功）。达到阈值时进入暂停窗口并返回 true。
func (t *autoPauseTracker) recordResult(ok bool, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok {
		t.streak = 0
		return false
	}
	t.streak++
	if t.streak >= t.threshold && !now.Before(t.pauseUntil) {
		t.pauseUntil = now.Add(t.wait)
		t.streak = 0
		Logf("[自动暂停] 连续失败达到 %d 次，注册暂停 %d 分钟（至 %s），到期自动继续", t.threshold, int(t.wait/time.Minute), t.pauseUntil.Format("15:04:05"))
		return true
	}
	return false
}

// pausedState 返回当前是否处于暂停窗口及剩余秒数（供进度/状态上报）。
func (t *autoPauseTracker) pausedState(now time.Time) (bool, int) {
	if t == nil {
		return false, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pauseUntil.IsZero() || !now.Before(t.pauseUntil) {
		return false, 0
	}
	return true, int(time.Until(t.pauseUntil).Seconds()) + 1
}

// waitIfPaused 在暂停窗口内阻塞至窗口结束（可取消）。返回 true 表示被取消。
func (t *autoPauseTracker) waitIfPaused(cancel *SafeCancel) bool {
	for {
		t.mu.Lock()
		until := t.pauseUntil
		t.mu.Unlock()
		if until.IsZero() || !time.Now().Before(until) {
			return false
		}
		d := time.Until(until)
		if d > 15*time.Second {
			d = 15 * time.Second
		}
		if sleepWithCancel(d, cancel) {
			return true
		}
	}
}

func captchaProxyForWorker(mode string, proxyPool []string, registrationProxy string, workerID int) string {
	mode = NormalizeCaptchaProxyMode(mode, true)
	switch mode {
	case CaptchaProxyModeDirect:
		return ""
	case CaptchaProxyModeSelected:
		if len(proxyPool) == 0 {
			return ""
		}
		return proxyPool[0]
	case CaptchaProxyModeGlobal:
		if len(proxyPool) == 0 {
			return ""
		}
		if workerID < 0 {
			workerID = -workerID
		}
		return proxyPool[workerID%len(proxyPool)]
	default:
		return registrationProxy
	}
}

// Configure sets unexported registrar fields used by Run.
func (r *GrokRegistrar) Configure(emailMode string, emailSvc EmailService, stepDelay, otpPollMS int, skipVerify bool) {
	if r == nil {
		return
	}
	r.emailMode = emailMode
	r.emailSvc = emailSvc
	r.stepDelay = stepDelay
	if otpPollMS <= 0 {
		otpPollMS = 500
	}
	r.otpPoll = time.Duration(otpPollMS) * time.Millisecond
	r.skipVerify = skipVerify
}

// RunBatch runs concurrent Grok registrations locally (control flow aligned with Kiro runGrokBatch).
func RunBatch(opts BatchOptions, cancel *SafeCancel) BatchProgress {
	if opts.Count <= 0 {
		opts.Count = 1
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	// No artificial concurrency ceiling — follow caller/settings (smart presets size against workers+proxies).
	if opts.BatchConfig == nil {
		opts.BatchConfig = DefaultBatchConfig()
	}
	SetHumanDelayEnabled(opts.BatchConfig.HumanDelay)
	if strings.TrimSpace(opts.EmailMode) == "" {
		opts.EmailMode = "mailtm"
	}
	if strings.TrimSpace(opts.Browser) == "" {
		opts.Browser = "chrome"
	}
	if strings.TrimSpace(opts.OSType) == "" {
		opts.OSType = "random"
	}
	if opts.Captcha.Provider == "" {
		opts.Captcha.Provider = CaptchaProviderEzSolver
	}

	// Fresh per-batch proxy traffic counters (service may still keep history aggregates).
	ResetTaskProxyTraffic()

	// ── 1) Resolve / pre-check proxy pool (Kiro: preCheckProxyPool before start) ──
	//
	// 只取「并发数（+headroom）」个代理作为工作集，其余留作未探测的储备。
	// 预检要经每个代理各发一次请求，住宅代理按请求/流量计费——并发 1 却把
	// 37 个代理全探一遍纯属烧额度。工作集里有槽被淘汰时再从储备即时补位
	// （见 refillProxyPool），所以不会因为一个代理挂了就断批。
	allProxies := append([]string(nil), opts.ProxyPool...)
	proxyPool, proxyReserve := splitProxyWorkingSet(allProxies, opts.Concurrency, opts.BatchConfig)
	// wantWorkingSize 是工作集的目标大小；淘汰后低于它就从储备补位。
	wantWorkingSize := len(proxyPool)
	if len(proxyReserve) > 0 {
		Logf("[Grok] [代理] 工作集 %d 个（并发=%d），储备 %d 个未预检，淘汰时自动补位",
			len(proxyPool), opts.Concurrency, len(proxyReserve))
	}
	if len(proxyPool) > 0 && !opts.SkipProxyPreCheck {
		checked := PreCheckProxyPool(proxyPool, 2, "[Grok] ")
		// 工作集被预检打空时，从储备里继续取下一批补齐，避免「刚好抽到几个坏槽」
		// 就直接判定整池不可用（旧实现预检全池所以不会遇到，现在必须自己兜）。
		for len(checked) == 0 && len(proxyReserve) > 0 {
			want := len(proxyPool)
			if want > len(proxyReserve) {
				want = len(proxyReserve)
			}
			nextBatch := proxyReserve[:want]
			proxyReserve = proxyReserve[want:]
			Logf("[Grok] [代理] 工作集全不可用，从储备补 %d 个重试预检（剩余储备 %d）",
				len(nextBatch), len(proxyReserve))
			checked = PreCheckProxyPool(nextBatch, 2, "[Grok] ")
		}
		proxyPool = checked
		if len(proxyPool) == 0 {
			Logf("[Grok] 所有代理都不可用, 中止任务")
			prog := BatchProgress{
				Total: opts.Count, Running: false,
				Message: "所有代理都不可用, 中止任务",
			}
			if opts.OnProgress != nil {
				opts.OnProgress(prog)
			}
			return prog
		}
	}

	Logf("[Grok] 开始批量注册: 数量=%d, 并发=%d, 间隔=%ds, 代理=%d个, 邮箱=%s, 浏览器=%s/%s, 打码=%s, 打码代理=%s",
		opts.Count, opts.Concurrency, opts.DelaySec, len(proxyPool),
		opts.EmailMode, opts.Browser, opts.OSType, opts.Captcha.Provider,
		NormalizeCaptchaProxyMode(opts.CaptchaProxyMode, true))

	var completed, success, fail int32
	// 连续失败自动暂停跟踪器（report 闭包需要它暴露暂停状态）。
	var pause *autoPauseTracker
	if opts.AutoPauseOnFailures {
		pause = newAutoPauseTracker(opts.AutoPauseFailThreshold, opts.AutoPauseWaitMinutes)
	}
	prog := BatchProgress{Total: opts.Count, Running: true, Message: "running"}
	var progressMu sync.Mutex
	batchDone := make(chan struct{})
	report := func(email, errMsg, msg string) {
		progressMu.Lock()
		defer progressMu.Unlock()
		prog.Completed = int(atomic.LoadInt32(&completed))
		prog.Success = int(atomic.LoadInt32(&success))
		prog.Fail = int(atomic.LoadInt32(&fail))
		prog.LastEmail = email
		prog.LastError = errMsg
		if msg != "" {
			prog.Message = msg
		}
		tx, rx, reqs := TaskProxyTraffic()
		prog.ProxyTxBytes = tx
		prog.ProxyRxBytes = rx
		prog.ProxyRequests = reqs
		if pause != nil {
			prog.AutoPaused, prog.AutoPauseRemainingSec = pause.pausedState(time.Now())
		}
		prog.Running = prog.Completed < opts.Count && (cancel == nil || !cancel.IsClosed())
		if opts.OnProgress != nil {
			opts.OnProgress(prog)
		}
	}
	report("", "", fmt.Sprintf("注册中… 代理=%d 并发=%d", len(proxyPool), opts.Concurrency))

	// ── 2) Proxy pool runtime (sticky + fail tracking + mid-batch eliminate) ──
	// max_per_ip = 同一代理 URL（动态住宅出口槽）累计成功注册上限。
	// 单账号：选中代理后，注册主链路 + Step9 验活全程使用同一 proxy URL（见 register.go）。
	// 达到上限后强制重连（新 TCP 经代理，动态住宅常换出口 IP），再继续。
	type proxyStat struct {
		Success int
		Fail    int
		TotalMs int64
	}
	var proxyMu sync.Mutex
	proxyStatsMap := make(map[string]*proxyStat)
	proxyFails := make(map[string]int)
	proxyLastUsed := make(map[string]time.Time)
	// 全局按代理 URL 累计成功数（跨 worker），真正限制「一个动态 IP/槽」上限
	proxySuccessTotal := make(map[string]int)
	proxyExitIP := make(map[string]string)
	// Seed exit IPs from precheck cache (best-effort labels for logs).
	for _, u := range proxyPool {
		if e, ok := getCachedProxy(u); ok && e.alive && e.ip != "" {
			proxyExitIP[u] = e.ip
		}
	}
	maxProxyFails := opts.BatchConfig.MaxProxyFails
	if maxProxyFails <= 0 {
		maxProxyFails = 3
	}
	// proxyCoolUntil 记录被判定「疑似限流」的代理挂起到什么时候。
	// 挂起期间 pickProxy 不选它；到期自动放回（不需要额外的清理协程）。
	proxyCoolUntil := make(map[string]time.Time)
	proxyCooldown := time.Duration(opts.BatchConfig.ProxyCooldownMinutes) * time.Minute
	initialPoolSize := len(proxyPool)
	maxPerIP := opts.BatchConfig.MaxPerIP
	if maxPerIP <= 0 {
		maxPerIP = 1
	}
	minInterval := time.Duration(opts.BatchConfig.MinProxyInterval) * time.Millisecond

	type stickySession struct {
		proxy string
	}
	var stickyMu sync.Mutex
	stickyByWorker := make(map[int]*stickySession)

	// forceProxyReconnect: 动态住宅代理「失败可重连 / 达上限换出口」。
	// 同一 host:port 槽位靠新 TCP/session 换出口；固定出口则仅重置计数并重新探测。
	// 返回值 exitChanged：重连后出口 IP 是否真的换了。
	// 固定出口代理换不掉 IP，被限流时继续用只会一直撞同一个限流 ——
	// 调用方靠这个信号决定要不要把它挂起冷却。
	forceProxyReconnect := func(proxy, reason string) (exitChanged bool) {
		if proxy == "" {
			return false
		}
		InvalidateProxyCache(proxy)
		oldIP := ""
		proxyMu.Lock()
		oldIP = proxyExitIP[proxy]
		delete(proxyLastUsed, proxy)
		proxySuccessTotal[proxy] = 0
		proxyFails[proxy] = 0
		proxyMu.Unlock()

		ip, ok := testProxyAlive(proxy)
		proxyMu.Lock()
		if ok && ip != "" {
			proxyExitIP[proxy] = ip
			putCachedProxy(proxy, true, ip)
		} else {
			delete(proxyExitIP, proxy)
		}
		proxyMu.Unlock()
		if ok {
			if oldIP != "" && ip != "" && oldIP != ip {
				Logf("[Grok] [动态代理] 重连换出口 reason=%s %s %s → %s (max_per_ip=%d)",
					reason, MaskProxy(proxy), oldIP, ip, maxPerIP)
				return true
			}
			Logf("[Grok] [动态代理] 重连完成 reason=%s %s exit=%s (max_per_ip=%d)",
				reason, MaskProxy(proxy), ip, maxPerIP)
			// oldIP 未知时无法判断是否换了出口，保守当作「换了」，
			// 避免把探测缓存缺失误判成固定出口而错误冷却。
			return oldIP == "" || ip == ""
		}
		Logf("[Grok] [动态代理] 重连探测失败 reason=%s %s（下次选用其它槽或再试）", reason, MaskProxy(proxy))
		return false
	}

	// coolingLocked 该代理是否处于限流冷却中（调用方须持 proxyMu）。
	// 到期即视为可用，不必显式清理 map。
	coolingLocked := func(proxy string, now time.Time) bool {
		return proxyIsCooling(proxyCoolUntil, proxy, now)
	}

	pickProxy := func() string {
		proxyMu.Lock()
		if len(proxyPool) == 0 {
			proxyMu.Unlock()
			return ""
		}
		// Prefer slots under max_per_ip; if all full, reconnect the least-recently-used full slot.
		now := time.Now()
		bestIdx := -1
		var bestTime time.Time
		fullIdx := -1
		var fullTime time.Time
		// 冷却中的代理全部跳过；但若整池都在冷却，绝不能返回空串
		// （那会让 worker 走无代理直连，等于用本机 IP 裸奔注册）——
		// 兜底选一个「最早到期」的继续用，宁可撞限流也不能泄露真实出口。
		if soonestIdx, soonest, allCooling := soonestCoolingIndex(proxyPool, proxyCoolUntil, now); allCooling {
			chosen := proxyPool[soonestIdx]
			proxyLastUsed[chosen] = now
			proxyMu.Unlock()
			Logf("[Grok] [代理限流] 全部 %d 个代理都在冷却中，暂时继续使用最早到期的 %s（%.0f 秒后到期）",
				len(proxyPool), MaskProxy(chosen), time.Until(soonest).Seconds())
			return chosen
		}
		for i, p := range proxyPool {
			if coolingLocked(p, now) {
				continue
			}
			if proxySuccessTotal[p] >= maxPerIP {
				t, exists := proxyLastUsed[p]
				if !exists {
					fullIdx = i
					fullTime = time.Time{}
					continue
				}
				if fullIdx == -1 || t.Before(fullTime) {
					fullIdx = i
					fullTime = t
				}
				continue
			}
			t, exists := proxyLastUsed[p]
			if !exists {
				bestIdx = i
				break
			}
			if minInterval > 0 && now.Sub(t) < minInterval {
				continue
			}
			if bestIdx == -1 || t.Before(bestTime) {
				bestIdx = i
				bestTime = t
			}
		}
		needReconnect := false
		if bestIdx < 0 {
			// All under-cap slots cooling or none available — fall back to LRU among non-full, else reconnect full.
			for i, p := range proxyPool {
				if coolingLocked(p, now) || proxySuccessTotal[p] >= maxPerIP {
					continue
				}
				t, ok := proxyLastUsed[p]
				if !ok {
					bestIdx = i
					break
				}
				if bestIdx == -1 || t.Before(bestTime) {
					bestIdx = i
					bestTime = t
				}
			}
		}
		if bestIdx < 0 {
			// Every slot hit max_per_ip → reconnect one and reuse it.
			// 只在未冷却的槽里挑；上面的 allCooling 分支已保证此处至少有一个可选。
			if fullIdx < 0 {
				live := make([]int, 0, len(proxyPool))
				for i, p := range proxyPool {
					if !coolingLocked(p, now) {
						live = append(live, i)
					}
				}
				if len(live) == 0 {
					bestIdx = rand.Intn(len(proxyPool)) // 理论不可达（allCooling 已拦），保底不 panic
				} else {
					bestIdx = live[rand.Intn(len(live))]
				}
			} else {
				bestIdx = fullIdx
			}
			needReconnect = true
		}
		chosen := proxyPool[bestIdx]
		if needReconnect {
			proxyMu.Unlock()
			forceProxyReconnect(chosen, "max_per_ip_all_slots")
			proxyMu.Lock()
		}
		proxyLastUsed[chosen] = time.Now()
		proxyMu.Unlock()
		return chosen
	}

	acquireStickyProxy := func(workerID int) (string, func(finalProxy string, ok bool)) {
		stickyMu.Lock()
		ss := stickyByWorker[workerID]
		if ss == nil {
			ss = &stickySession{}
			stickyByWorker[workerID] = ss
		}
		// Drop sticky if this URL already reached global max_per_ip successes.
		if ss.proxy != "" {
			proxyMu.Lock()
			atCap := proxySuccessTotal[ss.proxy] >= maxPerIP
			proxyMu.Unlock()
			if atCap {
				old := ss.proxy
				stickyMu.Unlock()
				forceProxyReconnect(old, "max_per_ip")
				stickyMu.Lock()
				ss.proxy = ""
			}
		}
		if ss.proxy == "" {
			stickyMu.Unlock()
			pxy := pickProxy()
			stickyMu.Lock()
			ss.proxy = pxy
		}
		proxy := ss.proxy
		stickyMu.Unlock()

		release := func(finalProxy string, success bool) {
			var reconnectProxy string
			stickyMu.Lock()
			cur := stickyByWorker[workerID]
			if cur == nil {
				stickyMu.Unlock()
				return
			}
			if !success {
				if cur.proxy != "" {
					Logf("[Grok] [动态代理] worker=%d proxy=%s 失败/取消，解除粘性以便重连换线", workerID, MaskProxy(cur.proxy))
				}
				cur.proxy = ""
				stickyMu.Unlock()
				return
			}
			if finalProxy == "" {
				stickyMu.Unlock()
				return
			}
			if cur.proxy != finalProxy {
				cur.proxy = finalProxy
			}
			proxyMu.Lock()
			proxySuccessTotal[finalProxy]++
			n := proxySuccessTotal[finalProxy]
			exit := proxyExitIP[finalProxy]
			proxyMu.Unlock()
			if exit != "" {
				Logf("[Grok] [动态代理] worker=%d proxy=%s exit=%s 累计成功 %d/%d（整流程同出口）",
					workerID, MaskProxy(finalProxy), exit, n, maxPerIP)
			} else {
				Logf("[Grok] [动态代理] worker=%d proxy=%s 累计成功 %d/%d（整流程同代理槽）",
					workerID, MaskProxy(finalProxy), n, maxPerIP)
			}
			if n >= maxPerIP {
				// 达上限：清粘性，强制重连换出口，供后续任务使用
				reconnectProxy = cur.proxy
				cur.proxy = ""
			}
			stickyMu.Unlock()
			if reconnectProxy != "" {
				forceProxyReconnect(reconnectProxy, "max_per_ip")
			}
		}
		return proxy, release
	}

	// errMsg 用于区分「限流」与「代理坏了」：前者代理自身是好的，
	// 换不掉出口时该冷却让位，而不是无限重连重试同一个被限的 IP。
	trackProxyResult := func(proxy, status, errMsg string, durationMs int64) {
		if proxy == "" {
			return
		}
		// cancelled / captcha / neutral: 不算代理失败，不淘汰、不计入 fail 统计
		if status == "skip" || status == "neutral" || status == "cancelled" {
			return
		}
		isSuccess := status == "success"
		proxyMu.Lock()
		ps, ok := proxyStatsMap[proxy]
		if !ok {
			ps = &proxyStat{}
			proxyStatsMap[proxy] = ps
		}
		ps.TotalMs += durationMs
		if isSuccess {
			ps.Success++
			proxyFails[proxy] = 0
			proxyMu.Unlock()
			return
		}
		// 真实失败：先记 fail，达阈值再验证；验证仍可用则重连保留，不可用才淘汰
		ps.Fail++
		proxyFails[proxy]++
		fails := proxyFails[proxy]
		needCheck := fails >= maxProxyFails
		proxyMu.Unlock()
		if !needCheck {
			if fails == 1 || fails == maxProxyFails-1 {
				Logf("[Grok] [诊断] 代理失败计数 %s = %d/%d (status=%s, %dms) — 可恢复将重连/换槽", MaskProxy(proxy), fails, maxProxyFails, status, durationMs)
			}
			return
		}
		InvalidateProxyCache(proxy)
		// 业务域名解析失败(DNS)是代理出口的确定性故障:
		// 即使连通性验证(只测 IP 站)通过,打码/注册也必然失败,跳过豁免直接淘汰。
		dnsDead := isProxyDNSFailure(errMsg)
		if dnsDead {
			Logf("[Grok] [代理淘汰] %s 业务域名解析失败(DNS), 跳过连通验证直接淘汰", MaskProxy(proxy))
		}
		if dnsDead || !VerifyProxyMidBatch(proxy, "[Grok] ") {
			proxyMu.Lock()
			newPool := make([]string, 0, len(proxyPool))
			removed := false
			for _, pxy := range proxyPool {
				if pxy != proxy {
					newPool = append(newPool, pxy)
				} else {
					removed = true
				}
			}
			if removed {
				proxyPool = newPool
				delete(proxyLastUsed, proxy)
				delete(proxySuccessTotal, proxy)
				delete(proxyExitIP, proxy)
				Logf("[Grok] [代理淘汰] %s 连续失败 %d 次且验证不可用, 已移除 (剩余 %d 个)", MaskProxy(proxy), maxProxyFails, len(proxyPool))
			}
			// 工作集掉到目标大小以下时从储备补位（储备此前未预检，这里才实探）。
			// 少了这一步，按并发数取用的工作集挂一个就可能直接断批。
			if removed && len(proxyReserve) > 0 && len(proxyPool) < wantWorkingSize {
				candidates := proxyReserve
				proxyMu.Unlock()
				picked, rest := takeProxyReserve(candidates)
				proxyMu.Lock()
				proxyReserve = rest
				if picked != "" {
					proxyPool = append(proxyPool, picked)
					if e, ok := getCachedProxy(picked); ok && e.alive && e.ip != "" {
						proxyExitIP[picked] = e.ip
					}
					Logf("[Grok] [代理补位] 已从储备启用 %s（工作集 %d 个，剩余储备 %d）",
						MaskProxy(picked), len(proxyPool), len(proxyReserve))
				}
			}
			empty := len(proxyPool) == 0 && initialPoolSize > 0
			proxyMu.Unlock()
			if empty {
				msg := "所有代理已淘汰（SOCKS 认证失败/连不上），任务已自动停止。请检查代理账密/余额或更换代理后重开"
				Logf("[Grok] ⚠ %s", msg)
				report("", msg, msg)
				if cancel != nil {
					cancel.Close()
				}
			}
		} else {
			// 仍可用：重置计数并强制重连（用户要求：代理失败可以重新连接）
			rateLimited := isRateLimitFailure(errMsg)
			if rateLimited {
				Logf("[Grok] [代理限流] %s 连续 %d 次被上游限流（代理自身连通正常）→ 尝试重连换出口",
					MaskProxy(proxy), maxProxyFails)
			} else {
				Logf("[Grok] [代理保留] %s 连续失败 %d 次但即时验证仍可用 → 重连后继续", MaskProxy(proxy), maxProxyFails)
			}
			proxyMu.Lock()
			proxyFails[proxy] = 0
			proxyMu.Unlock()
			exitChanged := forceProxyReconnect(proxy, "mid_batch_fail_recover")

			// 限流 + 重连换不掉出口 = 固定出口被掐。继续用只会一直撞同一个限流，
			// 此前这里会无限循环（计数清零 → 重连同 IP → 再失败）。挂起冷却并从
			// 储备补位顶上，到期自动放回。
			if rateLimited && !exitChanged && proxyCooldown > 0 {
				proxyMu.Lock()
				proxyCoolUntil[proxy] = time.Now().Add(proxyCooldown)
				liveCount := 0
				now := time.Now()
				for _, p := range proxyPool {
					if !coolingLocked(p, now) {
						liveCount++
					}
				}
				// 可用槽不足目标工作集时从储备补位（与淘汰路径同一套逻辑）
				var picked string
				if len(proxyReserve) > 0 && liveCount < wantWorkingSize {
					candidates := proxyReserve
					proxyMu.Unlock()
					p, rest := takeProxyReserve(candidates)
					proxyMu.Lock()
					proxyReserve = rest
					if p != "" {
						picked = p
						proxyPool = append(proxyPool, p)
						if e, ok := getCachedProxy(p); ok && e.alive && e.ip != "" {
							proxyExitIP[p] = e.ip
						}
					}
				}
				proxyMu.Unlock()
				Logf("[Grok] [代理限流] %s 出口 IP 未变（固定出口），已挂起 %d 分钟避让，可用 %d 个",
					MaskProxy(proxy), opts.BatchConfig.ProxyCooldownMinutes, liveCount)
				if picked != "" {
					Logf("[Grok] [代理补位] 已从储备启用 %s（工作集 %d 个，剩余储备 %d）",
						MaskProxy(picked), len(proxyPool), len(proxyReserve))
				}
			}
		}
	}

	// Stall diagnostics every 45s (Kiro)
	if cancel != nil {
		go func() {
			ticker := time.NewTicker(45 * time.Second)
			defer ticker.Stop()
			var lastCompleted int32
			start := time.Now()
			for {
				select {
				case <-cancel.Done():
					return
				case <-batchDone:
					return
				case <-ticker.C:
					c := atomic.LoadInt32(&completed)
					s := atomic.LoadInt32(&success)
					f := atomic.LoadInt32(&fail)
					if c >= int32(opts.Count) {
						return
					}
					proxyMu.Lock()
					aliveN := len(proxyPool)
					proxyMu.Unlock()
					if c == lastCompleted {
						// Prefer phase-aware hint — do NOT blame proxy when free captcha hangs.
						// EzSolver is host Chrome (not residential proxy); proxy retry won't fix captcha.
						sec := int(time.Since(start).Seconds())
						hint := "常见: 免费打码慢/CDP超时 · 等邮箱OTP · 注册提交慢（打码失败不会换代理，避免空烧住宅流量）"
						if sec <= 90 && s == 0 && f == 0 {
							hint = "当前多半在等邮箱验证码（CF Worker/KV 收信），不是代理故障"
						} else if f > 0 {
							hint = "近期失败偏多：优先看 CAPTCHA/EzSolver hard-timeout；代理仅在初始化/提交网络错误时换线重试"
						}
						msg := fmt.Sprintf("诊断: 约45s无新完成 completed=%d success=%d fail=%d 可用代理=%d/%d 已运行%ds（%s）",
							c, s, f, aliveN, initialPoolSize, sec, hint)
						Logf("[Grok] ⚠ %s", msg)
						report("", "", msg)
					} else {
						lastCompleted = c
					}
				}
			}
		}()
	}

	// ── 3) Workers ──
	jobs := make(chan int, opts.Count)
	for i := 0; i < opts.Count; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	workers := opts.Concurrency
	if workers > opts.Count {
		workers = opts.Count
	}

	pickEmailSvc := func(i, workerID int) EmailService {
		switch opts.EmailMode {
		case "outlook":
			if i < len(opts.EmailServices) {
				return opts.EmailServices[i]
			}
		case "icloud_api", "icloud":
			// iCloud 取码平台：engine 已构造池模式 EmailService（每并发槽一个）
			if i < len(opts.EmailServices) {
				return opts.EmailServices[i]
			}
		case "managed_temp":
			if i < len(opts.EmailServices) {
				return opts.EmailServices[i]
			}
		case "cfworker", "edu":
			if len(opts.EmailServices) > 0 {
				idx := i % len(opts.EmailServices)
				if strings.EqualFold(opts.CFPickMode, "random") {
					n := len(opts.EmailServices)
					r := int(time.Now().UnixNano()) ^ (i * 2654435761) ^ (workerID * 40503)
					if r < 0 {
						r = -r
					}
					idx = r % n
				}
				if src, ok := opts.EmailServices[idx].(*CFWorkerEmail); ok {
					return src.Clone()
				}
				return opts.EmailServices[idx]
			}
		}
		return nil
	}

	// 流水线预取器:worker 打码完成后为下一任务预取 solve(默认开启,可配置关闭)。
	var prefetch *CaptchaPrefetch
	if opts.BatchConfig.PipelineMode != nil && *opts.BatchConfig.PipelineMode {
		prefetch = NewCaptchaPrefetch()
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			if prefetch != nil {
				defer prefetch.Clear(workerID)
			}
			if opts.BatchConfig.StaggerSec > 0 && workerID > 0 {
				sleepWithCancel(time.Duration(opts.BatchConfig.StaggerSec*workerID)*time.Second, cancel)
			}
			for i := range jobs {
				if cancel != nil && cancel.IsClosed() {
					return
				}
				// 连续失败自动暂停：派发新任务前检查暂停窗口（多 worker 一起阻塞）。
				// 等待期间每 ~15s 醒来上报一次进度，让外部倒计时实时刷新。
				if pause != nil {
					for {
						if cancelled := pause.waitIfPaused(cancel); cancelled {
							return
						}
						if paused, _ := pause.pausedState(time.Now()); !paused {
							break
						}
						report("", "", "自动暂停中…")
					}
				}
				if opts.DelaySec > 0 && i > 0 {
					sleepWithCancel(time.Duration(opts.DelaySec)*time.Second, cancel)
				}

				taskStart := time.Now()
				usedProxy, stickyDone := acquireStickyProxy(workerID)
				proxyLabel := MaskProxy(usedProxy)

				if usedProxy == "" && initialPoolSize > 0 {
					Logf("[Grok] [%d/%d] ⚠ 代理池已全部淘汰，跳过", i+1, opts.Count)
					stickyDone("", false)
					atomic.AddInt32(&completed, 1)
					atomic.AddInt32(&fail, 1)
					report("", "代理池已空", "代理池已全部淘汰")
					if opts.OnResult != nil {
						opts.OnResult(RegisterResult{"status": "error", "error": "代理池已全部淘汰"})
					}
					continue
				}

				// Up to 3 attempts: CF 403 / captcha timeout → drop sticky proxy and re-pick.
				const maxJobAttempts = 3
				var result RegisterResult
				var lastProxy = usedProxy
				var lastProxyLabel = proxyLabel
				stickyRelease := stickyDone

				// One logical job = one device identity. Pick the browser/OS/UA profile
				// ONCE here; proxy retries reuse the same identity instead of picking a
				// new browser/version mid-job (which was an incoherent-fingerprint tell).
				jobProfile := PickBrowserProfile(opts.Browser, opts.OSType)

				var errMsgTry string
				for attempt := 0; attempt < maxJobAttempts; attempt++ {
					if cancel != nil && cancel.IsClosed() {
						return
					}
					if attempt > 0 {
						if stickyRelease != nil {
							stickyRelease(lastProxy, false)
						}
						if !isProxyIrrelevantFailure(errMsgTry) {
							InvalidateProxyCache(lastProxy)
							trackProxyResult(lastProxy, "error", errMsgTry, 0)
						}
						np, ndone := acquireStickyProxy(workerID)
						lastProxy, stickyRelease = np, ndone
						lastProxyLabel = MaskProxy(lastProxy)
						if lastProxy == "" && initialPoolSize > 0 {
							result = RegisterResult{"status": "error", "error": "代理池已空（重试换代理失败）"}
							break
						}
						Logf("[Grok] [%d/%d] 换代理重试 %d/%d → %s", i+1, opts.Count, attempt+1, maxJobAttempts, lastProxyLabel)
						sleepWithCancel(time.Duration(300*attempt)*time.Millisecond, cancel)
					} else {
						Logf("[Grok] [%d/%d] 开始注册 (代理: %s, worker=%d, 粘性成功额度=%d)", i+1, opts.Count, lastProxyLabel, workerID, maxPerIP)
					}

					// Reuse the job's immutable profile across proxy retries (see above).
					profile := jobProfile
					reg := NewGrokRegistrar(lastProxy, cancel, &profile)
					if opts.OnOTPVerified != nil {
						reg.SetOnOTPVerified(opts.OnOTPVerified)
					}

					var emailSvc EmailService
					switch opts.EmailMode {
					case "outlook", "cfworker", "edu":
						emailSvc = pickEmailSvc(i, workerID)
					case "managed_temp", "icloud_api", "icloud":
						// iCloud：engine 已构造池模式 EmailService（随机领/自动创建）
						emailSvc = pickEmailSvc(i, workerID)
						if emailSvc == nil {
							emailSvc = NewTempEmail(lastProxy)
						}
					default:
						emailSvc = NewMailTMEmail(lastProxy) // mail.tm 走注册同代理
					}
					if emailSvc != nil {
						if setter, ok := emailSvc.(interface{ SetCancel(*SafeCancel) }); ok {
							setter.SetCancel(cancel)
						}
						if setter, ok := emailSvc.(interface{ SetLogPrefix(string) }); ok {
							setter.SetLogPrefix("[Grok] ")
						}
						if setter, ok := emailSvc.(interface{ SetCodeExtractor(func(string) string) }); ok {
							setter.SetCodeExtractor(grokExtractCode)
						}
					}
					reg.Configure(opts.EmailMode, emailSvc, opts.StepDelay, opts.BatchConfig.OTPPollMs, opts.SkipVerify)

					cap := opts.Captcha
					cap.ProxyURL = captchaProxyForWorker(
						opts.CaptchaProxyMode,
						opts.CaptchaProxyPool,
						lastProxy,
						workerID,
					)
					// 流水线模式:注入预取器 + worker 编号 + 打码完成回调。
					// 回调在打码结果落定后触发(任务还剩等码/提交 ~6.5s),
					// 用当前粘性代理为下一任务提前 solve —— 8s 打码与等码/提交重叠。
					cap.Prefetch = prefetch
					cap.WorkerID = workerID
					cap.OnCaptchaDone = func(wid int, usedProxy string) {
						if prefetch == nil {
							return
						}
						next := usedProxy
						stickyMu.Lock()
						if ss := stickyByWorker[wid]; ss != nil && ss.proxy != "" {
							next = ss.proxy
						}
						stickyMu.Unlock()
						if next == "" {
							return
						}
						prefetch.Start(wid, next, cap, cancel)
					}
					cap.ProxyExplicit = true
					if cap.UserAgent == "" {
						cap.UserAgent = profile.UA
					}

					result = reg.Run(cap)
					if reg != nil && reg.client != nil {
						reg.client.Close()
					}
					if result == nil {
						result = RegisterResult{"status": "error", "error": "nil result"}
					}
					stTry, _ := result["status"].(string)
					if stTry == "success" || stTry == "cancelled" {
						break
					}
					errMsgTry, _ = result["error"].(string)
					// 打码/取消不是代理问题：不换出口（整流程保持同 IP）
					if isProxyIrrelevantFailure(errMsgTry) || isUserCancelledStatus(stTry, errMsgTry) {
						break
					}
					if !shouldRetryJobWithNewProxy(errMsgTry) || attempt == maxJobAttempts-1 {
						break
					}
					Logf("[Grok] [%d/%d] 代理侧可恢复失败，将重连/换槽: %s", i+1, opts.Count, truncStr(errMsgTry, 120))
				}

				st, _ := result["status"].(string)
				email, _ := result["email"].(string)
				errMsgForProxy, _ := result["error"].(string)
				durMs := time.Since(taskStart).Milliseconds()
				ok := st == "success"
				// cancelled / 打码侧失败不算代理挂;仅真实网络/CF/SOCKS 计 fail。
				// 例外:打码链路的 DNS 解析失败是代理出口故障,必须计 fail 走淘汰。
				proxyTrack := st
				switch {
				case isProxyDNSFailure(errMsgForProxy):
					proxyTrack = "dns_fail"
				case isRegionBlockedFailure(errMsgForProxy):
					// 地区封锁:出口位置不被接受,计失败让它冷却/淘汰,别再被选中
					proxyTrack = "region_blocked"
				case isUserCancelledStatus(st, errMsgForProxy):
					proxyTrack = "cancelled"
				case !ok && isProxyIrrelevantFailure(errMsgForProxy):
					proxyTrack = "skip"
				}
				trackProxyResult(lastProxy, proxyTrack, errMsgForProxy, durMs)
				if stickyRelease != nil {
					// 成功：累计 max_per_ip 并可能强制重连；失败/取消：解除粘性以便下一任务换线
					stickyRelease(lastProxy, ok)
				}

				atomic.AddInt32(&completed, 1)
				if ok {
					if pause != nil {
						pause.recordResult(true, time.Now())
					}
					result["registration_proxy"] = lastProxy
					atomic.AddInt32(&success, 1)
					Logf("[Grok] [%d/%d] ✓ 注册成功 %s (%.1fs, 代理=%s)", i+1, opts.Count, email, float64(durMs)/1000, lastProxyLabel)
					if opts.OnSuccess != nil {
						opts.OnSuccess(result)
					}
					report(email, "", "")
					// Internal handoff only. Never persist or expose a raw proxy URL (it may contain credentials).
					// 注意：必须在 OnResult 之后删除——engine 自动入库（SSO→OAuth 转换）
					// 需要读 registration_proxy 走同出口代理，删除过早会导致转换直连 x.ai
					// 被 DNS 污染拦截（lookup accounts.x.ai: no such host），入库成功率腰斩。
				} else if isUserCancelledStatus(st, errMsgForProxy) {
					// Stop/teardown must not inflate fail counters or look like "本亏".
					errMsg, _ := result["error"].(string)
					if errMsg == "" {
						errMsg = st
					}
					result["status"] = "cancelled"
					Logf("[Grok] [%d/%d] ⊘ 已取消 %s: %s (%.1fs, 代理=%s)", i+1, opts.Count, email, errMsg, float64(durMs)/1000, lastProxyLabel)
					report(email, "", "cancelled")
				} else {
					atomic.AddInt32(&fail, 1)
					if pause != nil {
						pause.recordResult(false, time.Now())
					}
					errMsg, _ := result["error"].(string)
					if errMsg == "" {
						errMsg = st
					}
					Logf("[Grok] [%d/%d] ✗ 失败 %s: %s (%.1fs, 代理=%s)", i+1, opts.Count, email, errMsg, float64(durMs)/1000, lastProxyLabel)
					report(email, errMsg, "")
				}
				if opts.OnResult != nil {
					opts.OnResult(result)
				}
				// 注册代理只传递给 OnResult（自动入库转换用），此后立即清除防持久化
				delete(result, "registration_proxy")
			}
		}(w)
	}
	wg.Wait()
	close(batchDone)

	// 批次结束即回收打码浏览器：引擎默认保留 standby 进程等下一次 solve，
	// 没有注册任务时这些 Chrome 白占内存（实测容器 14 个进程 / 2.3GB 常驻），
	// 而且下一批开跑时它们已经在啃内存，更容易撞资源闸门。
	// 硬回收（suspend=true）关掉全部浏览器进程并停泊池；下一次 /solve 到达时
	// 引擎自己 resume，不需要手动 prewarm。
	ReleaseCaptchaBrowsers(context.Background(), opts.Captcha)

	// ── 4) Summary ──
	proxyMu.Lock()
	if len(proxyStatsMap) > 0 {
		Logf("[Grok] ── 代理统计 ──")
		for proxy, ps := range proxyStatsMap {
			total := ps.Success + ps.Fail
			rate := 0
			if total > 0 {
				rate = ps.Success * 100 / total
			}
			avgSec := 0.0
			if total > 0 {
				avgSec = float64(ps.TotalMs) / float64(total) / 1000
			}
			Logf("[Grok]   %s  成功: %d/%d (%d%%)  平均耗时: %.1fs", MaskProxy(proxy), ps.Success, total, rate, avgSec)
		}
	}
	proxyMu.Unlock()

	progressMu.Lock()
	prog.Completed = int(atomic.LoadInt32(&completed))
	prog.Success = int(atomic.LoadInt32(&success))
	prog.Fail = int(atomic.LoadInt32(&fail))
	tx, rx, reqs := TaskProxyTraffic()
	prog.ProxyTxBytes = tx
	prog.ProxyRxBytes = rx
	prog.ProxyRequests = reqs
	prog.Running = false
	// Keep all-proxy-fail terminal messages so the service can schedule re-Start.
	if !isAllProxiesFailedMessageLocal(prog.Message) {
		prog.Message = fmt.Sprintf("done success=%d fail=%d", prog.Success, prog.Fail)
	}
	Logf("[Grok] 批量注册结束: success=%d fail=%d completed=%d/%d proxy_traffic=%.2fMB (tx=%.2f rx=%.2f reqs=%d)",
		prog.Success, prog.Fail, prog.Completed, opts.Count,
		float64(tx+rx)/1024/1024, float64(tx)/1024/1024, float64(rx)/1024/1024, reqs)
	if opts.OnProgress != nil {
		opts.OnProgress(prog)
	}
	finalProgress := prog
	progressMu.Unlock()
	return finalProgress
}

// proxyIsCooling 代理是否仍在限流冷却窗口内。到期即视为可用，
// 所以不需要额外的清理协程去删 map 项。
func proxyIsCooling(coolUntil map[string]time.Time, proxy string, now time.Time) bool {
	until, ok := coolUntil[proxy]
	return ok && now.Before(until)
}

// soonestCoolingIndex 找出「最早到期」的冷却代理，并报告是否整池都在冷却。
//
// 抽成纯函数是为了能单测这条安全不变式：**整池冷却时绝不能选不出代理**。
// pickProxy 返回空串会让 worker 走无代理直连 —— 用本机真实 IP 裸奔注册，
// 比撞限流严重得多。所以这里宁可返回一个还在冷却的代理继续用。
func soonestCoolingIndex(pool []string, coolUntil map[string]time.Time, now time.Time) (idx int, until time.Time, allCooling bool) {
	if len(pool) == 0 {
		return -1, time.Time{}, false
	}
	idx = 0
	until = coolUntil[pool[0]]
	for i, p := range pool {
		if !proxyIsCooling(coolUntil, p, now) {
			return -1, time.Time{}, false // 有可用的，交给正常挑选路径
		}
		if u := coolUntil[p]; u.Before(until) {
			idx, until = i, u
		}
	}
	return idx, until, true
}

func proxyLabel(p string) string {
	return MaskProxy(p)
}

// isAllProxiesFailedMessageLocal preserves terminal proxy-pool failure messages.
func isAllProxiesFailedMessageLocal(msg string) bool {
	m := strings.TrimSpace(msg)
	if m == "" {
		return false
	}
	for _, k := range []string{"所有代理都不可用", "所有代理已淘汰", "代理池已全部淘汰", "代理池已空"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
