package engine

import (
	"context"
	"fmt"

	captchapkg "github.com/umbraforge/desktop/internal/pkg/captcha"
)

// 打码并发（MAX_WORKERS / 浏览器数）如何决定。
//
// 语义与 sub2api 的 grok_register_service.go 完全对齐（decideAuralithConcurrency）：
//
//	Ez/Velora: MaxConcurrency 严格跟随设置（ezsolver_max_concurrency，
//	           <=0 视为 1）——不跟随注册并发。
//	Auralith:  0/auto = 跟随注册并发 1:1（注册并发=N ⇒ 打码闸=N）：
//	           - 设置值 >0：闸 = 设置值（显式上限）
//	           - 设置值 0：保留服务端 live workers，仅在不足时扩容
//	             （live < 注册并发 → 提到注册并发；live >= 注册并发 → 保持）
//
// 用户明确要求「注册并发设置 10，打码也是 10」，不搞 2 倍余量——
// 那会把 12 核机器拉起 20 个 Chrome，白白浪费资源。

// maxRegisterConcurrency 是注册并发与打码闸的绝对上限（与 sub2api 一致）。
const maxRegisterConcurrency = 50

// auralithConcurrencyDecision 一次 Auralith 并发决策的结果。
// Gate 是客户端并发闸（同时最多发多少个 solve 请求）；
// Workers 是打码器进程 worker 数；SetWorkers 表示是否要调用 SetWorkers 调整。
type auralithConcurrencyDecision struct {
	Gate       int
	Workers    int
	SetWorkers bool
}

// decideAuralithConcurrency 与 sub2api 同名单函数行为一致：
//   - explicit > 0：闸 = workers = explicit（显式上限）
//   - explicit == 0（auto）：保留 live；live 未知时用 max(register, 4)；
//     live < register 时提升到 register；live >= register 时保持不动
//
// register/explicit/live 都会被钳到 [0, maxRegisterConcurrency]。
func decideAuralithConcurrency(register, explicit, live int) auralithConcurrencyDecision {
	register = clampInt(1, register, maxRegisterConcurrency)
	explicit = clampInt(0, explicit, maxRegisterConcurrency)
	live = clampInt(0, live, maxRegisterConcurrency)
	if explicit > 0 {
		return auralithConcurrencyDecision{Gate: explicit, Workers: explicit, SetWorkers: true}
	}
	workers := live
	if workers == 0 {
		workers = maxInt(register, 4)
	}
	if workers < register {
		return auralithConcurrencyDecision{Gate: register, Workers: register, SetWorkers: true}
	}
	return auralithConcurrencyDecision{Gate: workers, Workers: workers}
}

// resolveCaptchaWorkers 计算 EzSolver/VeloraTurn 的并发闸（机器能力钳制在
// plugins 侧做）。语义与 sub2api 一致：**严格跟随设置**，0/未设置 → 1，
// 不跟随注册并发（Auralith 才走 1:1 跟随，见 decideAuralithConcurrency）。
// concurrency 参数保留仅为兼容调用方签名（Auralith 在 Start 里用
// decideAuralithConcurrency 覆盖，不经过这里）。
func resolveCaptchaWorkers(explicit, _ int) int {
	if explicit > 0 {
		return clampInt(1, explicit, maxRegisterConcurrency)
	}
	return 1
}

func clampInt(lo, v, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// CaptchaWorkerSink 接收打码并发上限（由 plugins.Center 实现）。
// 用接口注入而不是直接依赖 plugins 包，避免 engine → plugins 的耦合。
type CaptchaWorkerSink interface {
	SetMaxWorkers(id string, n int)
	// EnsureCaptchaReady 在开跑前确保该打码器可用：Docker 模式下容器停了就拉起，
	// 拉不起来则回落本机模式。返回一行给用户看的说明（空表示本来就正常）。
	//
	// 为什么必需：Docker 引擎重启（改 VM 规格 / colima restart / 开机）会让容器
	// 停在 Exited，而应用仍记着 docker 模式。此时每一次解题都连不上 8194，
	// 整批注册全部失败，日志里只看到解题超时，很难指向真正的原因。
	EnsureCaptchaReady(id string) (string, error)
}

// 打码插件 ID（与 plugins.PluginID 的字符串值一致）。
const (
	captchaPluginEzSolver   = "ezsolver"
	captchaPluginVeloraTurn = "veloraturn"
	captchaPluginAuralith   = "auralith"
)

// SetCaptchaWorkerSink 注入打码并发下发目标（main.go 里传 plugins.Center 的适配器）。
func (e *RegisterEngine) SetCaptchaWorkerSink(sink CaptchaWorkerSink) {
	e.workerSink = sink
}

// PushCaptchaWorkers 按配置里的默认并发下发一次（启动时用）。
func (e *RegisterEngine) PushCaptchaWorkers(cfg Config) {
	e.pushCaptchaWorkers(cfg, 0)
}

// ensureCaptchaReady 开跑前自愈打码器。provider 为空/auto 时按 ezsolver 处理。
func (e *RegisterEngine) ensureCaptchaReady(provider string) {
	if e.workerSink == nil {
		return
	}
	id := captchaPluginEzSolver
	switch provider {
	case "auralith", "au":
		id = captchaPluginAuralith
	case "veloraturn":
		id = captchaPluginVeloraTurn
	}
	msg, err := e.workerSink.EnsureCaptchaReady(id)
	if msg != "" {
		e.appendLog("[Captcha] " + msg)
	}
	if err != nil {
		e.appendLog(fmt.Sprintf("[Captcha] %s 自愈失败：%v", id, err))
	}
}

// pushCaptchaWorkers 把配置里的并发上限下发给插件中心，供下次启动打码进程时注入。
// concurrency<=0 表示还不知道本次注册并发，用配置里的默认并发。
// Ez/Velora 严格跟随设置（0→1）；Auralith 传 0=auto（不预设 MAX_WORKERS，
// 运行时由 decideAuralithConcurrency 按 live 决策后再 SetWorkers）。
func (e *RegisterEngine) pushCaptchaWorkers(cfg Config, concurrency int) {
	if e.workerSink == nil {
		return
	}
	if concurrency <= 0 {
		concurrency = cfg.DefaultConcurrency
	}
	ez := resolveCaptchaWorkers(cfg.EzSolverMaxConcur, concurrency)
	e.workerSink.SetMaxWorkers(captchaPluginEzSolver, ez)
	e.workerSink.SetMaxWorkers(captchaPluginVeloraTurn, ez)
	// Auralith：0=auto（不预设，保留 auto-tune/live），显式值才下发。
	// 显式值加 2 余量（无上限）：并发打码占满浏览器容量会排队拖慢吞吐，
	// 留空位给重试/波动；与热同步逻辑保持一致（引擎 auto budget 兜底）。
	aur := clampInt(0, cfg.AuralithMaxConcur, maxRegisterConcurrency)
	if aur > 0 {
		aur += 2
	}
	e.workerSink.SetMaxWorkers(captchaPluginAuralith, aur)
}

// SyncTarget 一次热同步的目标打码器。
type SyncTarget struct {
	ID         string // ezsolver | veloraturn | auralith
	URL        string
	APIKey     string
	TimeoutSec int
}

// SyncResult 单个引擎热同步结果。
type SyncResult struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Workers int    `json:"workers"`
	Message string `json:"message,omitempty"`
}

// SyncCaptchaWorkers 把并发热同步到运行中的打码引擎——等价于 web 版
// 「同步打码并发」按钮（sub2api SyncEzSolverWorkers 的桌面实现）：
// 直接调引擎的 /admin/workers API，**不用重启 docker、不用重建容器**。
//   - auralith:        AuralithClient.SetWorkers → POST {url}/admin/workers
//   - ezsolver/veloraturn: EzSolverClient.SetWorkers（同一兼容协议）
//
// 返回每个目标的结果（ok/workers/message），并写入注册日志。
func (e *RegisterEngine) SyncCaptchaWorkers(targets []SyncTarget, workers int) []SyncResult {
	out := make([]SyncResult, 0, len(targets))
	for _, t := range targets {
		r := SyncResult{ID: t.ID}
		timeout := t.TimeoutSec
		if timeout <= 0 {
			timeout = 15
		}
		switch t.ID {
		case "auralith", "au":
			ac, err := captchapkg.NewAuralithClientValidated(t.URL, timeout, t.APIKey)
			if err != nil {
				r.Message = err.Error()
				out = append(out, r)
				continue
			}
			snap := ac.SetWorkers(context.Background(), workers)
			r.OK, r.Workers, r.Message = snap.OK, snap.Workers, snap.Message
		default:
			ec := captchapkg.NewEzSolverClientWithKey(t.URL, timeout, t.APIKey)
			snap := ec.SetWorkers(context.Background(), workers)
			r.OK, r.Workers, r.Message = snap.OK, snap.Workers, snap.Message
		}
		if r.OK {
			e.appendLog(fmt.Sprintf("[Captcha] %s workers 热同步为 %d (url=%s) — 免重启", t.ID, r.Workers, t.URL))
		} else {
			e.appendLog(fmt.Sprintf("[Captcha] %s workers 热同步失败: %s (url=%s)", t.ID, r.Message, t.URL))
		}
		out = append(out, r)
	}
	return out
}
