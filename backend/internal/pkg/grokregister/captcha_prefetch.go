package grokregister

import (
	"sync"
	"time"
)

// CaptchaPrefetch 流水线预取器。
//
// 打码(solve)是注册流程最慢的一步(实测 7-10s),且只依赖 siteKey/proxy/UA,
// 不依赖邮箱 —— 因此 worker 可以在「打码完成 → 下一任务打码点」的窗口里
// 提前为下一轮发起 solve,让 8s 打码与等码/提交(6.5s)重叠:
//
//	稳态单任务 15s → ~10s,3 并发吞吐 ~12/分钟 → ~18/分钟。
//
// 安全边界:
//   - 预取绑定代理,Take 时代理不匹配 → 返回 nil,调用方走正常 solve(兜底);
//   - 预取结果失败/为空 → 与正常 solve 失败等价,走既有重试逻辑;
//   - Turnstile token 有效期 ~2 分钟,预取提前量仅秒级,不会过期;
//   - 多 worker 各自一个预取槽,引擎并发容量足够(闸=注册并发×2)。
type CaptchaPrefetch struct {
	mu    sync.Mutex
	slots map[int]*prefetchSlot
}

type prefetchSlot struct {
	proxy  string
	active bool
	done   chan prefetchResult
}

type prefetchResult struct {
	res *TurnstileSolveResult
	err error
	sec float64
}

func NewCaptchaPrefetch() *CaptchaPrefetch {
	return &CaptchaPrefetch{slots: make(map[int]*prefetchSlot)}
}

// Start 为 worker 发起一次预取(幂等:同代理已有活跃预取则跳过)。
func (p *CaptchaPrefetch) Start(workerID int, proxy string, opts CaptchaOptions, cancel *SafeCancel) {
	if p == nil || proxy == "" {
		return
	}
	p.mu.Lock()
	if slot, ok := p.slots[workerID]; ok && slot.active && slot.proxy == proxy {
		p.mu.Unlock()
		return
	}
	slot := &prefetchSlot{proxy: proxy, active: true, done: make(chan prefetchResult, 1)}
	p.slots[workerID] = slot
	p.mu.Unlock()

	go func() {
		t0 := time.Now()
		solveOpts := opts
		res, cerr := SolveTurnstileWithOptions(solveOpts, cancel)
		slot.done <- prefetchResult{res: res, err: cerr, sec: time.Since(t0).Seconds()}
		p.mu.Lock()
		slot.active = false
		p.mu.Unlock()
	}()
}

// Take 取走 worker 的预取结果通道;代理不匹配/无活跃预取/已取走 → nil。
// 返回的通道已开始运行,可能已完成(立即读出)或仍需等待。
func (p *CaptchaPrefetch) Take(workerID int, proxy string) chan prefetchResult {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	slot, ok := p.slots[workerID]
	if !ok || !slot.active || slot.proxy != proxy {
		return nil
	}
	slot.active = false
	return slot.done
}

// Clear 清空 worker 的预取槽(worker 退出/批次结束时调用,防泄漏)。
func (p *CaptchaPrefetch) Clear(workerID int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.slots, workerID)
	p.mu.Unlock()
}
