package grokregister

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CaptchaProvider selects how Grok Turnstile is solved.
// Kept channels:
//   - nextcaptcha / yescaptcha  (paid, proven)
//   - ezsolver                 (free self-hosted nodriver+Xvfb, proven on ARM)
//   - veloraturn               (free standalone Go service, EzSolver-compatible HTTP)
//   - auto                     (ezsolver → next → yes)
//   - none                     (empty token, for debug only)
//
// Removed (unreliable for x.ai managed Turnstile):
//   - flaresolverr / LocalTS go-rod inject
type CaptchaProvider string

const (
	CaptchaProviderNextCaptcha CaptchaProvider = "nextcaptcha"
	CaptchaProviderYesCaptcha  CaptchaProvider = "yescaptcha"
	// CaptchaProviderEzSolver is free self-hosted Turnstile solver
	// (nodriver + Xvfb, POST /solve → {"token":"..."}). Default :8192.
	CaptchaProviderEzSolver CaptchaProvider = "ezsolver"
	// CaptchaProviderVeloraTurn uses the standalone VeloraTurn Go service while
	// retaining the same POST /solve wire contract as EzSolver.
	CaptchaProviderVeloraTurn CaptchaProvider = "veloraturn"
	CaptchaProviderAuralith   CaptchaProvider = "auralith"
	CaptchaProviderAuto       CaptchaProvider = "auto"
	CaptchaProviderNone       CaptchaProvider = "none"

	// Deprecated aliases still accepted by normalizeCaptchaProvider → mapped to ezsolver.
	// CaptchaProviderFlareSolverr is kept only as a string constant for legacy settings
	// migration (UI no longer offers it).
	CaptchaProviderFlareSolverr CaptchaProvider = "flaresolverr"
)

// CaptchaOptions is passed into registration workers for one run.
type CaptchaOptions struct {
	Provider       CaptchaProvider
	YesCaptchaKey  string
	NextCaptchaKey string
	ProxyURL       string
	// ProxyExplicit distinguishes an intentional empty proxy (direct mode) from
	// legacy callers that expect the registrar proxy to be inherited.
	ProxyExplicit bool
	UserAgent     string
	SiteURL       string
	SiteKey       string
	EzSolverURL   string // optional override; default from settings / 127.0.0.1:8192
	// EzSolverAPIKey for external/public EzSolver hosts (loopback/private free on server).
	EzSolverAPIKey string
	AuralithURL    string
	AuralithAPIKey string
}

// EzSolverConfig is persisted under settings.captcha.ezsolver.
// Free Linux/no-desktop path: nodriver + Xvfb service (POST /solve).
type AuralithConfig struct {
	URL            string         `json:"url"`
	APIKey         string         `json:"api_key,omitempty"`
	Nodes          []AuralithNode `json:"nodes,omitempty"`
	TimeoutMS      int            `json:"timeout_ms"`
	MaxConcurrency int            `json:"max_concurrency"`
	Enabled        bool           `json:"enabled"`
	// ProxyRewrite is injected from process config and must never be persisted or exposed.
	ProxyRewrite map[string]string `json:"-"`
}
type AuralithNode struct {
	Name    string `json:"name,omitempty"`
	URL     string `json:"url"`
	APIKey  string `json:"api_key,omitempty"`
	Enabled bool   `json:"enabled"`
}

// EzSolverConfig is persisted under settings.captcha.ezsolver.
type EzSolverConfig struct {
	URL string `json:"url"` // primary, or comma/newline-separated multi host list
	// ExtraURLs optional secondary free solvers (local + ARM dual path).
	// Also accepted as comma-separated values inside URL.
	ExtraURLs []string `json:"extra_urls,omitempty"`
	// APIKey required by external clients when server has EZSOLVER_API_KEY set.
	// Loopback / private LAN peers on the solver still skip auth.
	APIKey         string         `json:"api_key,omitempty"`
	Nodes          []EzSolverNode `json:"nodes,omitempty"`
	TimeoutMS      int            `json:"timeout_ms"`      // HTTP client timeout for one solve
	MaxConcurrency int            `json:"max_concurrency"` // client-side gate (service has its own)
	Enabled        bool           `json:"enabled"`         // nil = default true when URL set
}

type EzSolverNode struct {
	Name    string `json:"name,omitempty"`
	URL     string `json:"url"`
	APIKey  string `json:"api_key,omitempty"`
	Enabled bool   `json:"enabled"`
}

// CaptchaSettings is the top-level captcha block in /api/settings.
// FlareSolverr field is intentionally omitted — legacy JSON is ignored on load.
type CaptchaSettings struct {
	DefaultProvider CaptchaProvider `json:"default_provider"`
	EzSolver        EzSolverConfig  `json:"ezsolver"`
	Auralith        AuralithConfig  `json:"auralith"`
}

// TurnstileSolveResult holds token + optional browser context.
type TurnstileSolveResult struct {
	Token     string
	Provider  CaptchaProvider
	UserAgent string
	Cookies   map[string]string
}

func normalizeCaptchaProvider(raw string) CaptchaProvider {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(CaptchaProviderNextCaptcha), "next", "nc":
		return CaptchaProviderNextCaptcha
	case string(CaptchaProviderEzSolver), "ez", "selfhost", "self-hosted", "free",
		// Legacy free paths → EzSolver (stock FS / LocalTS removed).
		string(CaptchaProviderFlareSolverr), "fs", "flare", "localts", "local":
		return CaptchaProviderEzSolver
	case string(CaptchaProviderVeloraTurn), "velora", "veloraturn-go":
		return CaptchaProviderVeloraTurn
	case string(CaptchaProviderAuralith):
		return CaptchaProviderAuralith
	case string(CaptchaProviderAuto):
		return CaptchaProviderAuto
	case string(CaptchaProviderNone), "off", "disabled", "skip":
		return CaptchaProviderNone
	case string(CaptchaProviderYesCaptcha), "yes", "yc":
		return CaptchaProviderYesCaptcha
	case "":
		// Empty request provider falls through to settings.default_provider.
		return ""
	default:
		return CaptchaProvider(strings.ToLower(strings.TrimSpace(raw)))
	}
}

func freeSolverLabel(provider CaptchaProvider) string {
	if provider == CaptchaProviderVeloraTurn {
		return "VeloraTurn Go"
	}
	return "EzSolver Python"
}

func (c EzSolverConfig) isEnabled() bool {
	if strings.TrimSpace(c.URL) == "" {
		return false
	}
	// Enabled defaults true when URL set; callers may force false.
	return c.Enabled
}

// resolveCaptchaOptions builds options for a Grok registration attempt.
// reqProvider/reqYesKey come from the register request body (may be empty).
// Paid keys always resolve from Settings (global), not from the Grok page form.
func resolveCaptchaOptions(reqProvider, reqYesKey, proxyURL, userAgent, siteURL, siteKey string) (CaptchaOptions, error) {
	settings := loadCaptchaSettings()
	provider := normalizeCaptchaProvider(reqProvider)
	if strings.TrimSpace(reqProvider) == "" || provider == "" {
		provider = settings.DefaultProvider
		if provider == "" {
			provider = CaptchaProviderEzSolver
		}
	}

	// Ignore per-request paid keys so secrets stay in Settings only.
	_ = reqYesKey
	yesKey := getYesCaptchaKey("", "grok_config")
	nextKey := getNextCaptchaKey("")
	yesEnabled := yescaptchaEnabledFor("grok")

	// Legacy toggle: if YesCaptcha is disabled for grok and provider is pure yes,
	// treat as none (empty token) unless caller explicitly chose free/auto/next.
	if !yesEnabled && provider == CaptchaProviderYesCaptcha {
		provider = CaptchaProviderNone
		yesKey = ""
	}
	if !yesEnabled {
		// Keep yes key empty so auto cannot fall back to Yes unless re-enabled.
		yesKey = ""
	}

	opts := CaptchaOptions{
		Provider:       provider,
		YesCaptchaKey:  yesKey,
		NextCaptchaKey: nextKey,
		ProxyURL:       strings.TrimSpace(proxyURL),
		UserAgent:      strings.TrimSpace(userAgent),
		SiteURL:        siteURL,
		SiteKey:        siteKey,
		EzSolverURL:    strings.TrimSpace(settings.EzSolver.URL),
		AuralithURL:    strings.TrimSpace(settings.Auralith.URL),
		AuralithAPIKey: strings.TrimSpace(settings.Auralith.APIKey),
	}
	if opts.SiteURL == "" {
		opts.SiteURL = grokSignUpURL
	}
	if opts.SiteKey == "" {
		opts.SiteKey = grokSiteKey
	}

	switch opts.Provider {
	case CaptchaProviderNone:
		return opts, nil
	case CaptchaProviderNextCaptcha:
		if opts.NextCaptchaKey == "" {
			return opts, fmt.Errorf("已选择 NextCaptcha 但未在设置中配置 API Key；可改用 YesCaptcha/EzSolver/auto")
		}
	case CaptchaProviderYesCaptcha:
		if opts.YesCaptchaKey == "" {
			return opts, fmt.Errorf("已选择 YesCaptcha 但未在设置中配置 API Key；可改用 NextCaptcha/EzSolver/auto，或关闭 Grok YesCaptcha 开关")
		}
	case CaptchaProviderAuralith:
		if !settings.Auralith.Enabled || strings.TrimSpace(opts.AuralithURL) == "" {
			return opts, fmt.Errorf("已选择 Auralith 但未启用或未配置 URL")
		}
	case CaptchaProviderEzSolver, CaptchaProviderVeloraTurn:
		if !settings.EzSolver.isEnabled() && strings.TrimSpace(opts.EzSolverURL) == "" {
			return opts, fmt.Errorf("已选择 %s 但未配置 URL", freeSolverLabel(opts.Provider))
		}
	case CaptchaProviderAuto:
		// Auto: EzSolver → Next → Yes.
	default:
		return opts, fmt.Errorf("未知 captcha_provider: %s（可用: nextcaptcha|yescaptcha|ezsolver|veloraturn|auralith|auto|none）", opts.Provider)
	}
	return opts, nil
}

// SolveTurnstileWithOptions solves Turnstile via the selected provider.
//
//	ezsolver: free self-hosted (proven for x.ai managed Turnstile)
//	nextcaptcha / yescaptcha: paid
//	auto: EzSolver → NextCaptcha → YesCaptcha
//	none: empty token
func SolveTurnstileWithOptions(opts CaptchaOptions, cancel *SafeCancel) (*TurnstileSolveResult, error) {
	if isCancelled(cancel) {
		return nil, fmt.Errorf("cancelled")
	}
	settings := loadCaptchaSettings()
	siteURL := opts.SiteURL
	if siteURL == "" {
		siteURL = grokSignUpURL
	}
	siteKey := opts.SiteKey
	if siteKey == "" {
		siteKey = grokSiteKey
	}

	switch opts.Provider {
	case CaptchaProviderNone:
		return &TurnstileSolveResult{Token: "", Provider: CaptchaProviderNone}, nil
	case CaptchaProviderNextCaptcha:
		tok, err := solveTurnstileNextCaptcha(opts.NextCaptchaKey, siteKey, siteURL, cancel)
		if err != nil {
			return nil, err
		}
		return &TurnstileSolveResult{Token: tok, Provider: CaptchaProviderNextCaptcha}, nil
	case CaptchaProviderYesCaptcha:
		tok, err := solveTurnstileYesCaptcha(opts.YesCaptchaKey, siteKey, siteURL, cancel)
		if err != nil {
			return nil, err
		}
		return &TurnstileSolveResult{Token: tok, Provider: CaptchaProviderYesCaptcha}, nil
	case CaptchaProviderAuralith:
		return solveTurnstileAuralith(settings.Auralith, opts, siteURL, siteKey, cancel)
	case CaptchaProviderEzSolver, CaptchaProviderVeloraTurn:
		// Free-only: never fall back to paid. Prefer pre-warmed token (zero Chrome wait).
		// NOTE: do NOT start background warmup here. A live solve must not spin up the
		// immortal filler — that was the leak (every solve re-armed target, and the loop
		// then hammered /solve forever after the batch stopped). The batch path arms
		// warmup explicitly via ConfigureEzTokenWarmup and tears it down via StopEzTokenWarmup.
		//
		// Plan A (opt-in): when SolveViaProxy is on, solve fresh through the account's
		// residential proxy (skip the datacenter pool). A/B on the 8GB host showed the
		// datacenter solve is actually faster + higher-success, so this is OFF by default.
		if !solveViaProxyEnabled() {
			if res := takeEzPooledToken(siteURL, siteKey); res != nil {
				res.Provider = opts.Provider
				return res, nil
			}
		}
		res, err := solveTurnstileEzSolver(settings.EzSolver, opts, siteURL, siteKey, cancel)
		if err == nil && res != nil && res.Token != "" {
			res.Provider = opts.Provider
			return res, nil
		}
		if err == nil {
			err = fmt.Errorf("EzSolver 未返回 token")
		}
		return nil, err
	case CaptchaProviderAuto:
		// Prefer free EzSolver (+ pool); only then paid.
		if settings.EzSolver.isEnabled() {
			// Consume a pre-warmed token if the batch path armed one; never start the
			// background filler from a live solve (see EzSolver case above).
			if !solveViaProxyEnabled() {
				if res := takeEzPooledToken(siteURL, siteKey); res != nil {
					return res, nil
				}
			}
			res, err := solveTurnstileEzSolver(settings.EzSolver, opts, siteURL, siteKey, cancel)
			if err == nil && res != nil && res.Token != "" {
				return res, nil
			}
			if err != nil {
				Logf("[Captcha] auto: EzSolver 失败: %v", err)
			}
		}
		if isCancelled(cancel) {
			return nil, fmt.Errorf("cancelled")
		}
		tok, provider, err := solveTurnstilePaidFallback(opts, siteKey, siteURL, cancel, "auto")
		if err != nil {
			return nil, fmt.Errorf("auto 失败: %v", err)
		}
		return &TurnstileSolveResult{Token: tok, Provider: provider}, nil
	default:
		return nil, fmt.Errorf("未知 captcha_provider: %s", opts.Provider)
	}
}

// ── EzSolver (free self-hosted) ──

var (
	// No fixed client Timeout — each solve uses request context from settings.
	ezHTTPClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        128,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
	ezGateMu    sync.Mutex
	ezHostGates = map[string]*ezHostGate{}

	// Free token warmup pool: pre-solve Turnstile so register workers rarely wait.
	// Tokens are single-use and ~5min TTL; we keep them < ezTokenMaxAge.
	ezPoolMu      sync.Mutex
	ezPool        []ezPooledToken
	ezPoolTarget  int
	ezPoolRunning bool
	ezPoolSiteKey string
	ezPoolSiteURL string
	ezPoolCfg     EzSolverConfig
	ezPoolOpts    CaptchaOptions
	// Backpressure: consecutive warmup failures → slow fill; success resets.
	ezWarmFailStreak int
	ezWarmPauseUntil time.Time
	// When true, background warmup yields all Chrome slots to live register solves.
	ezWarmPaused bool
	// ezPoolStopCh is closed by StopEzTokenWarmup to terminate the current filler
	// goroutine. Without it the loop was immortal (for{}), so "停止注册" left the
	// background filler hammering /solve forever and holding Chrome/RAM.
	ezPoolStopCh chan struct{}
	// ezWarmLastProgress is the last time the pool made progress (a token was
	// consumed OR a warm fill succeeded). Drives the idle self-exit so a warmup
	// with no downstream demand releases its slots instead of churning Chrome.
	ezWarmLastProgress time.Time

	// Remote EzSolver MAX_WORKERS snapshot (from GET /health); caps warmup fan-out.
	ezWorkersMu  sync.Mutex
	ezWorkersURL string
	ezWorkersN   int
	ezWorkersAt  time.Time

	// Multi-host free solver: in-flight + EMA latency for least-loaded pick.
	ezHostMu    sync.Mutex
	ezHostStats = map[string]*ezHostStat{}
)

const (
	ezTokenMaxAge = 200 * time.Second // turnstile ~300s; discard early
	// Soft headroom: keep some Chrome slots free for live register solves.
	// Settings MaxConcurrency is still the hard upper bound (no artificial 16).
	ezWarmReserveSlots = 1
	// Free path: fail fast on slow CF challenges then try another host/attempt.
	// Settings timeout remains the outer budget; each attempt is soft-capped.
	// 28s: local hard-timeout was ~33–35s; leave headroom then flip host.
	ezAttemptSoftCapSec = 35
	// ezWarmIdleTTL: with no progress (no consume + no successful fill) for this
	// long, the warmup loop self-terminates and drains the pool. Prevents the
	// old failure mode where the filler burned Chrome forever after a batch ended.
	ezWarmIdleTTL = 60 * time.Second
)

type ezHostStat struct {
	inflight      int
	emaSec        float64
	failStreak    int
	cooldownUntil time.Time
	lastOK        time.Time
}

type ezHostGate struct {
	sem chan struct{}
	max int
}

type ezPooledToken struct {
	Token    string
	SiteKey  string
	SiteURL  string
	Provider CaptchaProvider
	At       time.Time
}

// EnsureEzTokenWarmup starts (or resizes) the free Turnstile token buffer.
// Pool size follows settings MaxConcurrency exactly (no hard 16 cap).
func EnsureEzTokenWarmup(cfg EzSolverConfig, opts CaptchaOptions, siteURL, siteKey string) {
	ensureEzTokenWarmup(cfg, opts, siteURL, siteKey)
}

// ensureEzTokenWarmup starts background free solvers that keep a token buffer.
// Safe to call often; starts at most one filler loop per process.
// Pool target and parallel fill follow settings MaxConcurrency exactly (no hard caps).
// concurrency=1 skips BG fill (avoids stealing the sole worker).
func ensureEzTokenWarmup(cfg EzSolverConfig, opts CaptchaOptions, siteURL, siteKey string) {
	if strings.TrimSpace(siteKey) == "" || strings.TrimSpace(siteURL) == "" {
		return
	}
	target := cfg.MaxConcurrency
	if target < 1 {
		target = 1
	}
	// Single worker: skip background fill so register Step6 owns the only Chrome.
	if target <= 1 {
		return
	}
	newBase := ezSolverBaseURL(cfg, opts)
	solverLabel := freeSolverLabel(opts.Provider)
	ezPoolMu.Lock()
	oldBase := ezSolverBaseURL(ezPoolCfg, ezPoolOpts)
	urlChanged := ezPoolRunning && oldBase != "" && newBase != "" && oldBase != newBase
	if urlChanged {
		// Drop tokens solved against the previous host — avoid mixed-URL warm spam.
		ezPool = nil
		Logf("[Captcha] [%s] 打码 URL 切换 %s → %s，已清空预热池", solverLabel, oldBase, newBase)
	}
	ezPoolTarget = target
	ezPoolCfg = cfg
	ezPoolOpts = opts
	ezPoolSiteKey = siteKey
	ezPoolSiteURL = siteURL
	ezWarmLastProgress = time.Now()
	stopCh := startWarmupLoopLocked()
	already := stopCh == nil
	ezPoolMu.Unlock()
	if already {
		// Target/URL resized mid-run; loop re-reads cfg/opts each tick.
		return
	}
	Logf("[Captcha] [%s] 免费 token 预热池启动 target=%d url=%s（严格跟设置，无写死上限）", solverLabel, target, newBase)
	go ezTokenWarmupLoop(stopCh)
}

// ConfigureEzTokenWarmup enables background fill to an explicit burst target.
func ConfigureEzTokenWarmup(cfg EzSolverConfig, opts CaptchaOptions, siteURL, siteKey string, target int) {
	if target < 1 {
		target = 1
	}
	if cfg.MaxConcurrency < 2 {
		return
	}
	ezPoolMu.Lock()
	ezWarmPaused = false
	ezWarmFailStreak = 0
	ezWarmPauseUntil = time.Time{}
	ezPoolTarget = target
	ezPoolCfg = cfg
	ezPoolOpts = opts
	ezPoolSiteKey = siteKey
	ezPoolSiteURL = siteURL
	ezWarmLastProgress = time.Now()
	stopCh := startWarmupLoopLocked()
	ezPoolMu.Unlock()
	Logf("[Captcha] [%s] 批次预热启动 target=%d workers=%d url=%s", freeSolverLabel(opts.Provider), target, cfg.MaxConcurrency, ezSolverBaseURL(cfg, opts))
	if stopCh != nil {
		go ezTokenWarmupLoop(stopCh)
	}
}

// startWarmupLoopLocked marks the filler as running and returns a fresh stop
// channel to hand the new goroutine, or nil if a loop is already running.
// Caller must hold ezPoolMu.
func startWarmupLoopLocked() chan struct{} {
	if ezPoolRunning {
		return nil
	}
	ezPoolRunning = true
	stopCh := make(chan struct{})
	ezPoolStopCh = stopCh
	return stopCh
}

// PauseEzTokenWarmup stops background fill so register jobs own EzSolver capacity.
func PauseEzTokenWarmup(reason string) {
	ezPoolMu.Lock()
	solverLabel := freeSolverLabel(ezPoolOpts.Provider)
	ezWarmPaused = true
	ezPoolTarget = 0 // stop new fill; preserve already-solved fresh tokens
	ezPoolMu.Unlock()
	if reason != "" {
		Logf("[Captcha] [%s] 预热已暂停: %s", solverLabel, reason)
	}
}

// ResumeEzTokenWarmup re-enables background fill after batch ends.
// Deprecated: resuming after a batch left the filler running forever (immortal
// loop) which held Chrome/RAM. Prefer StopEzTokenWarmup so idle capacity frees.
// Kept as a thin no-op-ish resume for any legacy caller; it only clears the
// pause flag and does NOT restore a fill target (so an idle loop still self-exits).
func ResumeEzTokenWarmup() {
	ezPoolMu.Lock()
	solverLabel := freeSolverLabel(ezPoolOpts.Provider)
	ezWarmPaused = false
	ezWarmFailStreak = 0
	ezWarmPauseUntil = time.Time{}
	ezPoolMu.Unlock()
	Logf("[Captcha] [%s] 预热已恢复(仅清暂停标记，不恢复填充目标)", solverLabel)
}

// StopEzTokenWarmup terminates the background warmup goroutine and clears the
// token buffer. Safe to call multiple times / when not running. This is the fix
// for "停止注册后 EzSolver 仍被占用": stopping a batch must tear the filler down
// instead of resuming it, so the solver's Chrome workers go idle and free RAM.
func StopEzTokenWarmup(reason string) {
	ezPoolMu.Lock()
	solverLabel := freeSolverLabel(ezPoolOpts.Provider)
	if ezPoolStopCh != nil {
		select {
		case <-ezPoolStopCh:
			// already closed by a self-exit
		default:
			close(ezPoolStopCh)
		}
		ezPoolStopCh = nil
	}
	ezPoolRunning = false
	ezWarmPaused = false
	ezPoolTarget = 0
	ezPool = nil
	ezWarmFailStreak = 0
	ezWarmPauseUntil = time.Time{}
	ezPoolMu.Unlock()
	if reason != "" {
		Logf("[Captcha] [%s] 预热已停止并清空库存: %s", solverLabel, reason)
	}
}

// EzTokenPoolSize returns the number of fresh buffered tokens for one challenge.
func EzTokenPoolSize(siteURL, siteKey string) int {
	now := time.Now()
	ezPoolMu.Lock()
	defer ezPoolMu.Unlock()
	alive := ezPool[:0]
	matched := 0
	for _, e := range ezPool {
		if e.Token == "" || now.Sub(e.At) > ezTokenMaxAge {
			continue
		}
		alive = append(alive, e)
		if (siteKey == "" || e.SiteKey == "" || e.SiteKey == siteKey) &&
			(siteURL == "" || e.SiteURL == "" || e.SiteURL == siteURL) {
			matched++
		}
	}
	ezPool = alive
	return matched
}

// WaitForEzTokenWarmup blocks before proxy-consuming registration begins.
func WaitForEzTokenWarmup(target int, timeout time.Duration, cancel *SafeCancel) int {
	if target < 1 {
		return 0
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n := EzTokenPoolSize(grokSignUpURL, grokSiteKey)
		if n >= target || isCancelled(cancel) {
			return n
		}
		if cancel == nil {
			time.Sleep(500 * time.Millisecond)
		} else {
			select {
			case <-cancel.Done():
				return n
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return EzTokenPoolSize(grokSignUpURL, grokSiteKey)
}

func takeEzPooledToken(siteURL, siteKey string) *TurnstileSolveResult {
	now := time.Now()
	ezPoolMu.Lock()
	defer ezPoolMu.Unlock()
	for i := 0; i < len(ezPool); {
		e := ezPool[i]
		if now.Sub(e.At) > ezTokenMaxAge || e.Token == "" {
			ezPool = append(ezPool[:i], ezPool[i+1:]...)
			continue
		}
		if (siteKey != "" && e.SiteKey != "" && e.SiteKey != siteKey) ||
			(siteURL != "" && e.SiteURL != "" && e.SiteURL != siteURL) {
			i++
			continue
		}
		// pop i
		ezPool = append(ezPool[:i], ezPool[i+1:]...)
		// Consumption = live demand; keep the filler alive (resets idle self-exit).
		ezWarmLastProgress = now
		age := now.Sub(e.At).Seconds()
		provider := e.Provider
		if provider == "" {
			provider = CaptchaProviderEzSolver
		}
		Logf("[Captcha] [%s] 使用预热 token age=%.0fs pool_left=%d", freeSolverLabel(provider), age, len(ezPool))
		return &TurnstileSolveResult{Token: e.Token, Provider: provider}
	}
	return nil
}

// probeEzSolverWorkers returns live MAX_WORKERS from solver /health (cached ~12s).
// 0 means unknown (caller falls back conservatively).
func probeEzSolverWorkers(base string) int {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return 0
	}
	ezWorkersMu.Lock()
	if ezWorkersURL == base && time.Since(ezWorkersAt) < 12*time.Second && ezWorkersN > 0 {
		n := ezWorkersN
		ezWorkersMu.Unlock()
		return n
	}
	ezWorkersMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return 0
	}
	resp, err := ezHTTPClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		Workers    int `json:"workers"`
		MaxWorkers int `json:"max_workers"`
	}
	_ = json.Unmarshal(raw, &out)
	n := out.Workers
	if n <= 0 {
		n = out.MaxWorkers
	}
	if n > 0 {
		ezWorkersMu.Lock()
		ezWorkersURL = base
		ezWorkersN = n
		ezWorkersAt = time.Now()
		ezWorkersMu.Unlock()
	}
	return n
}

func ezSolverBaseURL(cfg EzSolverConfig, opts CaptchaOptions) string {
	urls := ezSolverURLs(cfg, opts)
	if len(urls) == 0 {
		return "http://127.0.0.1:8192"
	}
	return urls[0]
}

// ezSolverURLs returns unique free-solver bases. Enabled nodes are authoritative;
// URL/ExtraURLs remain a compatibility path for configurations saved before nodes
// were introduced. Legacy local-only configurations still auto-append the ARM
// fallback, while an explicit local-only node selection must remain local-only.
func ezSolverURLs(cfg EzSolverConfig, opts CaptchaOptions) []string {
	raw := make([]string, 0, 4)
	hasEnabledNode := false
	for _, node := range cfg.Nodes {
		if node.Enabled && strings.TrimSpace(node.URL) != "" {
			hasEnabledNode = true
			raw = append(raw, node.URL)
		}
	}
	if !hasEnabledNode {
		if s := strings.TrimSpace(opts.EzSolverURL); s != "" {
			raw = append(raw, s)
		}
		if s := strings.TrimSpace(cfg.URL); s != "" {
			raw = append(raw, s)
		}
		raw = append(raw, cfg.ExtraURLs...)
	}
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, piece := range raw {
		for _, part := range strings.FieldsFunc(piece, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '|'
		}) {
			u := strings.TrimRight(strings.TrimSpace(part), "/")
			if u == "" {
				continue
			}
			key := strings.ToLower(u)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return []string{DefaultEzSolverURL, DefaultRemoteEzSolverURL}
	}
	// Auto dual applies only to legacy URL-only settings. Node selections are explicit.
	if !hasEnabledNode && allLocalEzSolverHosts(out) {
		remote := strings.TrimRight(DefaultRemoteEzSolverURL, "/")
		key := strings.ToLower(remote)
		if _, ok := seen[key]; !ok {
			out = append(out, remote)
			Logf("[Captcha] [EzSolver] 仅本机 URL，已自动附加远程 free: %s（本机 hard-timeout 会快切）", remote)
		}
	}
	return out
}

func ezSolverAPIKey(cfg EzSolverConfig, base string) string {
	want := strings.ToLower(strings.TrimRight(strings.TrimSpace(base), "/"))
	for _, node := range cfg.Nodes {
		got := strings.ToLower(strings.TrimRight(strings.TrimSpace(node.URL), "/"))
		if node.Enabled && got == want && strings.TrimSpace(node.APIKey) != "" {
			return strings.TrimSpace(node.APIKey)
		}
	}
	return strings.TrimSpace(cfg.APIKey)
}

func allLocalEzSolverHosts(urls []string) bool {
	if len(urls) == 0 {
		return true
	}
	for _, u := range urls {
		if !isLocalEzSolverHost(u) {
			return false
		}
	}
	return true
}

func isLocalEzSolverHost(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	host := u
	if i := strings.LastIndex(u, ":"); i >= 0 {
		host = u[:i]
	}
	switch host {
	case "127.0.0.1", "localhost", "0.0.0.0", "::1",
		"172.17.0.1", "172.18.0.1", "172.19.0.1", "172.20.0.1",
		"host.docker.internal":
		return true
	}
	return false
}

func pickEzSolverURLs(urls []string) []string {
	if len(urls) <= 1 {
		return urls
	}
	type scored struct {
		u string
		s float64
	}
	now := time.Now()
	items := make([]scored, 0, len(urls))
	ezHostMu.Lock()
	for _, u := range urls {
		st := ezHostStats[u]
		if st == nil {
			st = &ezHostStat{emaSec: 12}
			ezHostStats[u] = st
		}
		score := float64(st.inflight)*20 + st.emaSec
		if st.failStreak > 0 {
			score += float64(st.failStreak) * 15
		}
		if now.Before(st.cooldownUntil) {
			score += 200
		}
		items = append(items, scored{u: u, s: score})
	}
	ezHostMu.Unlock()
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && items[j].s < items[j-1].s {
			items[j], items[j-1] = items[j-1], items[j]
			j--
		}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.u
	}
	return out
}

func ezHostBegin(url string) {
	ezHostMu.Lock()
	st := ezHostStats[url]
	if st == nil {
		st = &ezHostStat{emaSec: 12}
		ezHostStats[url] = st
	}
	st.inflight++
	ezHostMu.Unlock()
}

func ezHostEnd(url string, ok bool, elapsedSec float64) {
	ezHostMu.Lock()
	st := ezHostStats[url]
	if st == nil {
		st = &ezHostStat{emaSec: 12}
		ezHostStats[url] = st
	}
	if st.inflight > 0 {
		st.inflight--
	}
	if ok {
		if elapsedSec < 0.5 {
			elapsedSec = 0.5
		}
		if st.emaSec <= 0 {
			st.emaSec = elapsedSec
		} else {
			st.emaSec = st.emaSec*0.7 + elapsedSec*0.3
		}
		st.failStreak = 0
		st.cooldownUntil = time.Time{}
		st.lastOK = time.Now()
	} else {
		st.failStreak++
		// Hard/slow failures: cool the host so pickEzSolverURLs prefers the other end.
		cd := time.Duration(12*st.failStreak) * time.Second
		if cd > 60*time.Second {
			cd = 60 * time.Second
		}
		st.cooldownUntil = time.Now().Add(cd)
		if st.emaSec < 50 {
			st.emaSec = 50
		}
	}
	ezHostMu.Unlock()
}

// warmSleep sleeps for d but returns true immediately if stopCh is closed, so a
// StopEzTokenWarmup takes effect without waiting out the sleep.
func warmSleep(stopCh chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-stopCh:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stopCh:
		return true
	case <-t.C:
		return false
	}
}

// warmupStopped reports whether this loop should terminate: stop channel closed,
// or a newer loop has superseded this one (ezPoolStopCh swapped).
func warmupStopped(stopCh chan struct{}) bool {
	select {
	case <-stopCh:
		return true
	default:
	}
	ezPoolMu.Lock()
	superseded := ezPoolStopCh != stopCh
	ezPoolMu.Unlock()
	return superseded
}

func ezTokenWarmupLoop(stopCh chan struct{}) {
	for {
		if warmupStopped(stopCh) {
			return
		}
		ezPoolMu.Lock()
		target := ezPoolTarget
		cfg := ezPoolCfg
		opts := ezPoolOpts
		siteKey := ezPoolSiteKey
		siteURL := ezPoolSiteURL
		pauseUntil := ezWarmPauseUntil
		failStreak := ezWarmFailStreak
		lastProgress := ezWarmLastProgress
		// prune expired + near-expiry churn protection: keep only fresh tokens
		now := time.Now()
		alive := ezPool[:0]
		for _, e := range ezPool {
			if now.Sub(e.At) <= ezTokenMaxAge && e.Token != "" {
				alive = append(alive, e)
			}
		}
		ezPool = alive
		have := len(ezPool)
		ezPoolMu.Unlock()

		// Idle self-exit: no token consumed and no successful fill for a while.
		// This is the safety net that stops the filler from burning Chrome/RAM
		// forever when there is no live demand (the original leak) — even if the
		// caller forgot to StopEzTokenWarmup.
		if !lastProgress.IsZero() && now.Sub(lastProgress) > ezWarmIdleTTL {
			ezPoolMu.Lock()
			if ezPoolStopCh == stopCh {
				ezPoolStopCh = nil
				ezPoolRunning = false
				ezPoolTarget = 0
				ezPool = nil
			}
			ezPoolMu.Unlock()
			Logf("[Captcha] [%s] 预热空闲 %.0fs 无消费/无进展，自动停止并释放槽位（无需手动停）", freeSolverLabel(opts.Provider), now.Sub(lastProgress).Seconds())
			return
		}

		if siteKey == "" || siteURL == "" {
			if warmSleep(stopCh, 2*time.Second) {
				return
			}
			continue
		}
		if !pauseUntil.IsZero() && now.Before(pauseUntil) {
			if warmSleep(stopCh, time.Until(pauseUntil)) {
				return
			}
			continue
		}
		ezPoolMu.Lock()
		paused := ezWarmPaused
		ezPoolMu.Unlock()
		if paused {
			// Live register owns the solver — do not steal slots.
			if warmSleep(stopCh, 2*time.Second) {
				return
			}
			continue
		}
		need := target - have
		if need <= 0 {
			// Pool full — idle longer to avoid burning Chrome for expired churn.
			if warmSleep(stopCh, 3*time.Second) {
				return
			}
			continue
		}
		// When pool is already ≥80% full, trickle-fill (1 at a time) to cut idle churn.
		if target > 0 && have*5 >= target*4 {
			need = 1
		}

		// Adaptive fill: respect remote workers + leave reserve for live register.
		// Settings MaxConcurrency remains the hard ceiling (target); we only back off under load.
		fill := need
		if fill < 1 {
			fill = 1
		}
		base := ezSolverBaseURL(cfg, opts)
		remoteW := probeEzSolverWorkers(base)
		capByRemote := 0
		if remoteW > 0 {
			// Leave ≥50% of remote workers free for live register (min reserve 1).
			reserve := remoteW / 2
			if reserve < ezWarmReserveSlots {
				reserve = ezWarmReserveSlots
			}
			capByRemote = remoteW - reserve
			if capByRemote < 1 {
				capByRemote = 1
			}
			if fill > capByRemote {
				fill = capByRemote
			}
		}
		// On failure streak, halve parallelism (floor 1).
		if failStreak > 0 {
			backoff := failStreak
			if backoff > 4 {
				backoff = 4
			}
			reduced := fill >> backoff
			if reduced < 1 {
				reduced = 1
			}
			fill = reduced
		}
		if fill > need {
			fill = need
		}
		if remoteW > 0 {
			Logf("[Captcha] [%s] 预热填池 fill=%d need=%d pool=%d/%d remote_workers=%d fail_streak=%d",
				freeSolverLabel(opts.Provider), fill, need, have, target, remoteW, failStreak)
		}

		type warmOutcome struct {
			ok  bool
			err error
		}
		outcomes := make(chan warmOutcome, fill)
		var wg sync.WaitGroup
		for i := 0; i < fill; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// nil cancel: warmup is best-effort background
				res, err := solveTurnstileEzSolver(cfg, opts, siteURL, siteKey, nil)
				if err != nil || res == nil || res.Token == "" {
					outcomes <- warmOutcome{ok: false, err: err}
					return
				}
				ezPoolMu.Lock()
				if len(ezPool) < ezPoolTarget {
					ezPool = append(ezPool, ezPooledToken{
						Token:    res.Token,
						SiteKey:  siteKey,
						SiteURL:  siteURL,
						Provider: opts.Provider,
						At:       time.Now(),
					})
					Logf("[Captcha] [%s] 预热入库 pool=%d/%d", freeSolverLabel(opts.Provider), len(ezPool), ezPoolTarget)
				}
				ezPoolMu.Unlock()
				outcomes <- warmOutcome{ok: true}
			}()
		}
		wg.Wait()
		close(outcomes)
		okN, failN := 0, 0
		var lastErr error
		for o := range outcomes {
			if o.ok {
				okN++
			} else {
				failN++
				if o.err != nil {
					lastErr = o.err
				}
			}
		}
		ezPoolMu.Lock()
		if okN > 0 {
			ezWarmFailStreak = 0
			ezWarmPauseUntil = time.Time{}
			// Successful fill = progress; keep the loop alive past the idle TTL.
			ezWarmLastProgress = time.Now()
		}
		if failN > 0 {
			ezWarmFailStreak++
			// Log at most summary (avoid N identical deadline lines).
			if lastErr != nil {
				Logf("[Captcha] [%s] 预热批次失败 ok=%d fail=%d streak=%d: %v", freeSolverLabel(opts.Provider), okN, failN, ezWarmFailStreak, lastErr)
			}
			// Pause 2s, 4s, 8s… cap 30s after repeated failures.
			pause := time.Duration(2<<min(ezWarmFailStreak-1, 3)) * time.Second
			if pause > 30*time.Second {
				pause = 30 * time.Second
			}
			ezWarmPauseUntil = time.Now().Add(pause)
		}
		ezPoolMu.Unlock()
		// Brief pause so register workers can claim gate slots between batches.
		if failN == 0 {
			if warmSleep(stopCh, 400*time.Millisecond) {
				return
			}
		} else {
			if warmSleep(stopCh, 800*time.Millisecond) {
				return
			}
		}
	}
}

func ezSolverHostGate(host string, max int) *ezHostGate {
	if max <= 0 {
		max = 1
	}
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	ezGateMu.Lock()
	gate := ezHostGates[host]
	if gate == nil || gate.max != max {
		gate = &ezHostGate{sem: make(chan struct{}, max), max: max}
		ezHostGates[host] = gate
		Logf("[Captcha] [FreeSolver] host=%s concurrency gate=%d", host, max)
	}
	ezGateMu.Unlock()
	return gate
}

func acquireEzSolverHostSlot(host string, max int, cancel *SafeCancel, wait time.Duration) (func(), bool, error) {
	gate := ezSolverHostGate(host, max)
	if wait <= 0 {
		select {
		case gate.sem <- struct{}{}:
			return func() { <-gate.sem }, true, nil
		default:
			return nil, false, nil
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	if cancel == nil {
		select {
		case gate.sem <- struct{}{}:
		case <-timer.C:
			return nil, false, fmt.Errorf("EzSolver client queue timeout")
		}
	} else {
		select {
		case gate.sem <- struct{}{}:
		case <-cancel.Done():
			return nil, false, fmt.Errorf("cancelled")
		case <-timer.C:
			return nil, false, fmt.Errorf("EzSolver client queue timeout")
		}
	}
	return func() { <-gate.sem }, true, nil
}

func acquireAnyEzSolverHost(hosts []string, max int, cancel *SafeCancel, wait time.Duration) (string, func(), error) {
	if len(hosts) == 0 {
		return "", nil, fmt.Errorf("EzSolver URL 为空")
	}
	deadline := time.Now().Add(wait)
	for {
		for _, host := range hosts {
			release, ok, err := acquireEzSolverHostSlot(host, max, cancel, 0)
			if err != nil {
				return "", nil, err
			}
			if ok {
				return host, release, nil
			}
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return "", nil, fmt.Errorf("EzSolver client queue timeout")
		}
		pause := 25 * time.Millisecond
		if remain < pause {
			pause = remain
		}
		if cancel == nil {
			time.Sleep(pause)
			continue
		}
		select {
		case <-cancel.Done():
			return "", nil, fmt.Errorf("cancelled")
		case <-time.After(pause):
		}
	}
}

// solveTurnstileEzSolver calls free EzSolver host(s):
//
//	POST {base}/solve  {"sitekey","siteurl","timeout"} → {"token","elapsed"} | {"error"}
//
// Multi-host: URL may be comma-separated and/or cfg.ExtraURLs. Each host is
// soft-capped so a slow CF challenge frees the slot; the request context is
// additionally capped by the remaining settings timeout budget.
func ezSolverAttemptTimeout(remaining time.Duration) int {
	sec := int(remaining.Seconds())
	if sec < 12 {
		return 12
	}
	if sec < ezAttemptSoftCapSec {
		return sec
	}
	return ezAttemptSoftCapSec
}

func shouldRetryEzSolverError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"hard-timeout", "soft-timeout", "cdp/page hung",
		"browser disconnected", "target closed", "websocket",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func solveTurnstileEzSolver(cfg EzSolverConfig, opts CaptchaOptions, siteURL, siteKey string, cancel *SafeCancel) (*TurnstileSolveResult, error) {
	if isCancelled(cancel) {
		return nil, fmt.Errorf("cancelled")
	}
	provider := opts.Provider
	if provider != CaptchaProviderVeloraTurn {
		provider = CaptchaProviderEzSolver
	}
	solverLabel := freeSolverLabel(provider)
	urls := pickEzSolverURLs(ezSolverURLs(cfg, opts))
	timeoutMS := cfg.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 120000
	}
	// Outer budget from settings. Each configured host is attempted once; account-level
	// retry decides whether a fresh solve is useful, avoiding 120s repeated host loops.
	budget := time.Duration(timeoutMS) * time.Millisecond
	deadline := time.Now().Add(budget)

	maxConc := cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 1
	}
	// Only forward the residential proxy to the solver when SolveViaProxy is enabled;
	// otherwise solve on the datacenter IP (faster + higher success per A/B).
	proxyForSolve := ""
	if solveViaProxyEnabled() {
		proxyForSolve = strings.TrimSpace(opts.ProxyURL)
	}
	var lastErr error
	remainingHosts := append([]string{}, urls...)
	for len(remainingHosts) > 0 {
		if isCancelled(cancel) {
			return nil, fmt.Errorf("cancelled")
		}
		queueBudget := time.Until(deadline)
		if queueBudget < 6*time.Second {
			break
		}
		base, release, err := acquireAnyEzSolverHost(remainingHosts, maxConc, cancel, queueBudget)
		if err != nil {
			lastErr = err
			break
		}
		remain := time.Until(deadline)
		if remain < 6*time.Second {
			release()
			break
		}
		// Give normal successful challenges their full observed window. The Python
		// solver must receive this value unchanged (35s, never silently 15s).
		attemptSec := ezSolverAttemptTimeout(remain)

		for i, host := range remainingHosts {
			if host == base {
				remainingHosts = append(remainingHosts[:i], remainingHosts[i+1:]...)
				break
			}
		}
		res, err := postEzSolverOnce(
			base, ezSolverAPIKey(cfg, base), siteURL, siteKey, attemptSec,
			time.Until(deadline), cancel, proxyForSolve, provider,
		)
		release()
		if err == nil && res != nil && res.Token != "" {
			return res, nil
		}
		if err != nil {
			lastErr = err
			Logf("[Captcha] [%s] host=%s 失败 remaining_hosts=%d: %v", solverLabel, base, len(remainingHosts), err)
			// A CDP/page hang is local browser state, not a permanent endpoint error.
			// Retry this host exactly once; the service has already recycled the worker.
			if shouldRetryEzSolverError(err) {
				remain = time.Until(deadline)
				if remain >= 6*time.Second {
					// Re-enter the per-host gate. Calling postEzSolverOnce directly here
					// would let retries exceed MaxConcurrency under load.
					retryRelease, retryOK, retryGateErr := acquireEzSolverHostSlot(base, maxConc, cancel, remain)
					if retryGateErr != nil {
						lastErr = retryGateErr
					} else if retryOK {
						remain = time.Until(deadline)
						if remain >= 6*time.Second {
							retrySec := ezSolverAttemptTimeout(remain)
							Logf("[Captcha] [%s] host=%s CDP/慢挑战可恢复，执行唯一一次新浏览器重试 timeout=%ds", solverLabel, base, retrySec)
							retryRes, retryErr := postEzSolverOnce(
								base, ezSolverAPIKey(cfg, base), siteURL, siteKey, retrySec,
								time.Until(deadline), cancel, proxyForSolve, provider,
							)
							retryRelease()
							if retryErr == nil && retryRes != nil && retryRes.Token != "" {
								return retryRes, nil
							}
							if retryErr != nil {
								lastErr = retryErr
								Logf("[Captcha] [%s] host=%s 唯一恢复重试失败: %v", solverLabel, base, retryErr)
							}
						} else {
							retryRelease()
						}
					}
				}
			}
		}
		if isCancelled(cancel) {
			return nil, fmt.Errorf("cancelled")
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%s 未返回 token", solverLabel)
}

func postEzSolverOnce(base, apiKey, siteURL, siteKey string, solveTimeoutSec int, maxWait time.Duration, cancel *SafeCancel, proxyURL string, provider CaptchaProvider) (*TurnstileSolveResult, error) {
	solverLabel := freeSolverLabel(provider)
	if solveTimeoutSec < 10 {
		solveTimeoutSec = 10
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("%s URL 为空", solverLabel)
	}
	reqBody := map[string]interface{}{
		"sitekey":    siteKey,
		"siteurl":    siteURL,
		"websiteURL": siteURL,
		"websiteKey": siteKey,
		"timeout":    solveTimeoutSec,
	}
	// Plan A: solve the Turnstile in-context on the SAME residential proxy that
	// registration uses. Datacenter-IP solves get harder CF challenges and hit
	// hard-timeout (the dominant prod failure); a residential IP gets an easier
	// challenge → higher solve rate. The solver runs a local relay to add SOCKS5
	// auth (Chrome can't). Empty proxy = solve direct (legacy behavior).
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		reqBody["proxy"] = proxyURL
	}
	body, _ := json.Marshal(reqBody)
	endpoint := base + "/solve"
	Logf("[Captcha] [%s] POST %s site=%s key=%s... (attempt_timeout=%ds proxy=%v)", solverLabel, endpoint, siteURL, truncateStr(siteKey, 12), solveTimeoutSec, proxyURL != "")

	httpWait := time.Duration(solveTimeoutSec)*time.Second + 12*time.Second
	if maxWait <= 0 {
		return nil, fmt.Errorf("%s 总超时已耗尽", solverLabel)
	}
	if maxWait < httpWait {
		httpWait = maxWait
	}
	ctx, cancelHTTP := context.WithTimeout(context.Background(), httpWait)
	defer cancelHTTP()
	if cancel != nil {
		go func() {
			select {
			case <-cancel.Done():
				cancelHTTP()
			case <-ctx.Done():
			}
		}()
	}

	ezHostBegin(base)
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		ezHostEnd(base, false, 0)
		return nil, fmt.Errorf("%s 构造请求失败: %v", solverLabel, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "keep-alive")
	// Each public node may have an independent key; private peers ignore it server-side.
	if k := strings.TrimSpace(apiKey); k != "" {
		req.Header.Set("X-Api-Key", k)
		req.Header.Set("X-EzSolver-Token", k)
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := ezHTTPClient.Do(req)
	if err != nil {
		ezHostEnd(base, false, time.Since(t0).Seconds())
		if isCancelled(cancel) {
			return nil, fmt.Errorf("cancelled")
		}
		return nil, fmt.Errorf("%s 请求失败 (%s): %v", solverLabel, endpoint, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(t0).Seconds()
	if resp.StatusCode != 200 {
		ezHostEnd(base, false, elapsed)
		preview := string(raw)
		if len(preview) > 240 {
			preview = preview[:240]
		}
		return nil, fmt.Errorf("%s HTTP %d: %s", solverLabel, resp.StatusCode, preview)
	}

	var out struct {
		Token    string  `json:"token"`
		Elapsed  float64 `json:"elapsed"`
		Error    string  `json:"error"`
		Solution struct {
			Token string `json:"token"`
		} `json:"solution"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		ezHostEnd(base, false, elapsed)
		preview := string(raw)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("%s 响应解析失败: %v; body=%s", solverLabel, err, preview)
	}
	if out.Error != "" {
		ezHostEnd(base, false, elapsed)
		return nil, fmt.Errorf("%s 错误: %s", solverLabel, shortFSMessage(out.Error))
	}
	tok := strings.TrimSpace(out.Token)
	if tok == "" {
		tok = strings.TrimSpace(out.Solution.Token)
	}
	if tok == "" {
		tok = strings.TrimSpace(out.Data.Token)
	}
	if len(tok) < 20 {
		ezHostEnd(base, false, elapsed)
		preview := string(raw)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("%s 未返回有效 token: %s", solverLabel, preview)
	}
	// Prefer server-reported elapsed when present.
	if out.Elapsed > 0 {
		elapsed = out.Elapsed
	}
	ezHostEnd(base, true, elapsed)
	Logf("[Captcha] [%s] 拿到 turnstile_token len=%d elapsed=%.1fs host=%s", solverLabel, len(tok), elapsed, base)
	return &TurnstileSolveResult{
		Token:    tok,
		Provider: provider,
	}, nil
}

// solveTurnstilePaidFallback tries NextCaptcha first, then YesCaptcha.
func solveTurnstilePaidFallback(opts CaptchaOptions, siteKey, siteURL string, cancel *SafeCancel, stage string) (string, CaptchaProvider, error) {
	var errs []string
	if strings.TrimSpace(opts.NextCaptchaKey) != "" {
		Logf("[Captcha] %s: 尝试 NextCaptcha", stage)
		if isCancelled(cancel) {
			return "", "", fmt.Errorf("cancelled")
		}
		tok, err := solveTurnstileNextCaptcha(opts.NextCaptchaKey, siteKey, siteURL, cancel)
		if err == nil && tok != "" {
			return tok, CaptchaProviderNextCaptcha, nil
		}
		if err != nil {
			errs = append(errs, "Next="+err.Error())
			Logf("[Captcha] %s: NextCaptcha 失败: %v", stage, err)
		}
	}
	if strings.TrimSpace(opts.YesCaptchaKey) != "" {
		Logf("[Captcha] %s: 尝试 YesCaptcha", stage)
		if isCancelled(cancel) {
			return "", "", fmt.Errorf("cancelled")
		}
		tok, err := solveTurnstileYesCaptcha(opts.YesCaptchaKey, siteKey, siteURL, cancel)
		if err == nil && tok != "" {
			return tok, CaptchaProviderYesCaptcha, nil
		}
		if err != nil {
			errs = append(errs, "Yes="+err.Error())
			Logf("[Captcha] %s: YesCaptcha 失败: %v", stage, err)
		}
	}
	if len(errs) == 0 {
		return "", "", fmt.Errorf("无可用收费打码 Key（请在设置配置 NextCaptcha/YesCaptcha）")
	}
	return "", "", fmt.Errorf("%s", strings.Join(errs, "; "))
}

func solveTurnstileNextCaptcha(apiKey, siteKey, siteURL string, cancel *SafeCancel) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", fmt.Errorf("NextCaptcha API Key 为空")
	}
	if siteURL == "" {
		siteURL = grokSiteURL
	}
	// Prefer origin root for TurnstileTaskProxyless compatibility.
	if strings.HasPrefix(siteURL, grokSiteURL) {
		siteURL = grokSiteURL
	}
	httpClient := &http.Client{Timeout: 120 * time.Second}
	if isCancelled(cancel) {
		return "", fmt.Errorf("cancelled")
	}

	taskBody, _ := json.Marshal(map[string]interface{}{
		"clientKey": apiKey,
		"task": map[string]interface{}{
			"type":       "TurnstileTaskProxyless",
			"websiteURL": siteURL,
			"websiteKey": siteKey,
		},
	})
	resp, err := httpClient.Post("https://api.nextcaptcha.com/createTask", "application/json", strings.NewReader(string(taskBody)))
	if err != nil {
		return "", fmt.Errorf("创建 NextCaptcha Turnstile 任务失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var createResp struct {
		ErrorID   int    `json:"errorId"`
		ErrorCode string `json:"errorCode"`
		ErrorDesc string `json:"errorDescription"`
		TaskID    any    `json:"taskId"`
	}
	json.Unmarshal(body, &createResp)
	if createResp.ErrorID != 0 {
		return "", fmt.Errorf("NextCaptcha 任务错误: %s (%s)", createResp.ErrorDesc, createResp.ErrorCode)
	}
	taskID := strings.TrimSpace(fmt.Sprint(createResp.TaskID))
	if taskID == "" || taskID == "<nil>" {
		return "", fmt.Errorf("NextCaptcha TaskID 为空")
	}

	Logf("[Captcha] [NextCaptcha] TaskID: %s, 轮询中...", taskID)
	for i := 0; i < 40; i++ {
		if isCancelled(cancel) {
			return "", fmt.Errorf("cancelled")
		}
		sleepWithCancel(3*time.Second, cancel)
		if isCancelled(cancel) {
			return "", fmt.Errorf("cancelled")
		}

		resultBody, _ := json.Marshal(map[string]string{
			"clientKey": apiKey,
			"taskId":    taskID,
		})
		rResp, err := httpClient.Post("https://api.nextcaptcha.com/getTaskResult", "application/json", strings.NewReader(string(resultBody)))
		if err != nil {
			continue
		}
		rBody, _ := io.ReadAll(rResp.Body)
		rResp.Body.Close()

		var result struct {
			ErrorID  int    `json:"errorId"`
			Status   string `json:"status"`
			Solution struct {
				Token string `json:"token"`
			} `json:"solution"`
			ErrorDesc string `json:"errorDescription"`
		}
		json.Unmarshal(rBody, &result)
		if result.Status == "ready" && result.Solution.Token != "" {
			return result.Solution.Token, nil
		}
		if result.ErrorID != 0 {
			return "", fmt.Errorf("NextCaptcha 任务错误: %s", result.ErrorDesc)
		}
	}
	return "", fmt.Errorf("NextCaptcha Turnstile 等待超时 (120s)")
}

func solveTurnstileYesCaptcha(apiKey, siteKey, siteURL string, cancel *SafeCancel) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", fmt.Errorf("YesCaptcha API Key 为空")
	}
	if siteURL == "" {
		siteURL = grokSiteURL
	}
	// Prefer origin root for YesCaptcha TurnstileTaskProxyless compatibility.
	if strings.HasPrefix(siteURL, grokSiteURL) {
		siteURL = grokSiteURL
	}
	httpClient := &http.Client{Timeout: 120 * time.Second}
	if isCancelled(cancel) {
		return "", fmt.Errorf("cancelled")
	}

	taskBody, _ := json.Marshal(map[string]interface{}{
		"clientKey": apiKey,
		"task": map[string]interface{}{
			"type":       "TurnstileTaskProxyless",
			"websiteURL": siteURL,
			"websiteKey": siteKey,
		},
	})
	resp, err := httpClient.Post("https://api.yescaptcha.com/createTask", "application/json", strings.NewReader(string(taskBody)))
	if err != nil {
		return "", fmt.Errorf("创建 Turnstile 任务失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var createResp struct {
		ErrorID   int    `json:"errorId"`
		ErrorCode string `json:"errorCode"`
		ErrorDesc string `json:"errorDescription"`
		TaskID    string `json:"taskId"`
	}
	json.Unmarshal(body, &createResp)
	if createResp.ErrorID != 0 {
		return "", fmt.Errorf("Turnstile 任务错误: %s (%s)", createResp.ErrorDesc, createResp.ErrorCode)
	}
	if createResp.TaskID == "" {
		return "", fmt.Errorf("Turnstile TaskID 为空")
	}

	Logf("[Captcha] [YesCaptcha] TaskID: %s, 轮询中...", createResp.TaskID)
	for i := 0; i < 40; i++ {
		if isCancelled(cancel) {
			return "", fmt.Errorf("cancelled")
		}
		sleepWithCancel(3*time.Second, cancel)
		if isCancelled(cancel) {
			return "", fmt.Errorf("cancelled")
		}

		resultBody, _ := json.Marshal(map[string]string{
			"clientKey": apiKey,
			"taskId":    createResp.TaskID,
		})
		rResp, err := httpClient.Post("https://api.yescaptcha.com/getTaskResult", "application/json", strings.NewReader(string(resultBody)))
		if err != nil {
			continue
		}
		rBody, _ := io.ReadAll(rResp.Body)
		rResp.Body.Close()

		var result struct {
			ErrorID  int    `json:"errorId"`
			Status   string `json:"status"`
			Solution struct {
				Token string `json:"token"`
			} `json:"solution"`
			ErrorDesc string `json:"errorDescription"`
		}
		json.Unmarshal(rBody, &result)
		if result.Status == "ready" && result.Solution.Token != "" {
			return result.Solution.Token, nil
		}
		if result.ErrorID != 0 {
			return "", fmt.Errorf("Turnstile 任务错误: %s", result.ErrorDesc)
		}
	}
	return "", fmt.Errorf("Turnstile 等待超时 (120s)")
}

// shortFSMessage collapses multi-line errors for logs (kept name for call sites).
func shortFSMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	msg = strings.ReplaceAll(msg, "\r", " ")
	msg = strings.ReplaceAll(msg, "\n", " ")
	for strings.Contains(msg, "  ") {
		msg = strings.ReplaceAll(msg, "  ", " ")
	}
	if idx := strings.Index(msg, "Stacktrace:"); idx > 0 {
		msg = strings.TrimSpace(msg[:idx])
	}
	if len(msg) > 220 {
		msg = msg[:220] + "..."
	}
	return msg
}

// applyTurnstileBrowserContext optionally syncs solver UA/cookies into the registrar client.
// EzSolver currently returns token only; kept for future cookie-aware free solvers.
// applyTurnstileBrowserContext is intentionally a NO-OP for session state.
//
// It used to overwrite r.profile.UA and inject solver cookies AFTER the sign-up
// page GET, gRPC send-code, OTP and verify had already gone out with the original
// UA — creating a UA jump inside one session (an obvious anti-bot tell). The token
// is valid regardless of the solver's own UA/cookies, so the coherent behavior is:
// keep the registration session's fingerprint fixed and never mutate it here.
// Kept as a no-op so call sites stay stable; a later strict mode validates the
// solver context instead of mutating the session.
func applyTurnstileBrowserContext(r *GrokRegistrar, res *TurnstileSolveResult, sync bool) {
	_ = r
	_ = res
	_ = sync
}

// testEzSolverConnectivity pings EzSolver with a lightweight POST /solve to a dummy page.
// Used by settings UI "测试连通".
func testEzSolverConnectivity(rawURL string) (map[string]interface{}, error) {
	base := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if base == "" {
		cfg := loadCaptchaSettings()
		base = strings.TrimRight(strings.TrimSpace(cfg.EzSolver.URL), "/")
	}
	if base == "" {
		base = "http://127.0.0.1:8192"
	}
	endpoint := base + "/solve"
	// Short probe: real solve is expensive; just check service accepts POST.
	// Use a well-known demo sitekey if needed — here we only verify HTTP layer with invalid key
	// and accept either token or structured error (not connection refused).
	payload, _ := json.Marshal(map[string]interface{}{
		"sitekey": "0x4AAAAAAAhr9JGVDZbrZOo0",
		"siteurl": "https://accounts.x.ai/sign-up",
		"timeout": 15,
	})
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return map[string]interface{}{
			"ok":  false,
			"url": base,
		}, fmt.Errorf("EzSolver 不可达: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[string]interface{}{
		"ok":           resp.StatusCode == 200 || resp.StatusCode == 400 || resp.StatusCode == 500,
		"url":          base,
		"status":       resp.StatusCode,
		"body_preview": truncateStr(string(body), 160),
	}
	// Prefer actual token success if returned quickly.
	var parsed struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	if len(parsed.Token) > 20 {
		out["ok"] = true
		out["token_len"] = len(parsed.Token)
		out["message"] = "EzSolver 已返回 token"
		return out, nil
	}
	if resp.StatusCode == 200 || resp.StatusCode == 400 || resp.StatusCode == 500 {
		out["ok"] = true
		out["message"] = "EzSolver 服务在线（探测请求已响应）"
		return out, nil
	}
	return out, fmt.Errorf("EzSolver HTTP %d", resp.StatusCode)
}

// testFlareSolverrConnectivity kept as thin alias so old API route does not 500;
// redirects meaning to EzSolver health.
func testFlareSolverrConnectivity(rawURL string) (map[string]interface{}, error) {
	// If caller still passes :8191, rewrite to EzSolver default.
	u := strings.TrimSpace(rawURL)
	if u == "" || strings.Contains(u, ":8191") {
		u = loadCaptchaSettings().EzSolver.URL
	}
	return testEzSolverConnectivity(u)
}

// truncateStr is shared by captcha logs (also used by turnstile remnants if any).
func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}
