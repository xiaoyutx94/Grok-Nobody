package grokregister

import (
	"strings"
	"sync"
)

// CaptchaRuntime is injectable captcha settings (replaces Kiro dbGetSettings).
type CaptchaRuntime struct {
	DefaultProvider CaptchaProvider
	EzSolver        EzSolverConfig
	Auralith        AuralithConfig
	YesCaptchaKey   string
	NextCaptchaKey  string
	YesEnabled      bool
	// SolveViaProxy routes the Turnstile solve through the registration's own
	// residential proxy (Plan A). Real-machine A/B showed datacenter solve is
	// actually faster and higher-success than proxy solve (proxy adds latency →
	// more soft-timeouts), so this defaults OFF. The plumbing (Go proxy field +
	// solver local relay) is kept for opt-in experimentation per proxy pool.
	SolveViaProxy bool
	// CaptchaRetry: 打码失败自动重试次数(config ezsolver_captcha_retry);默认5,冲100%成功率。
	CaptchaRetry  int
	AuralithRetry int
}

var (
	captchaMu      sync.RWMutex
	captchaRuntime = CaptchaRuntime{
		DefaultProvider: CaptchaProviderEzSolver,
		EzSolver: EzSolverConfig{
			URL:            DefaultEzSolverURL,
			TimeoutMS:      120000,
			MaxConcurrency: 1, // default until Start injects settings ezsolver_max_concurrency
			Enabled:        true,
		},
		Auralith:   AuralithConfig{URL: DefaultAuralithURL, TimeoutMS: 120000, MaxConcurrency: 1, Enabled: true},
		YesEnabled: true,
	}
)

const DefaultAuralithURL = "http://172.18.0.1:8194"

// DefaultEzSolverURL matches Kiro free solver (loopback; Docker uses 172.18.0.1).
const DefaultEzSolverURL = "http://127.0.0.1:8192"

// DefaultRemoteEzSolverURL is the free ARM EzSolver used as dual-path failover.
// Local Chrome often hard-timeouts under CF load; ARM is tried next automatically.
const DefaultRemoteEzSolverURL = "http://151.145.76.6:8192"

// DefaultDualEzSolverURL is local Docker gateway + ARM free solvers.
const DefaultDualEzSolverURL = "http://172.18.0.1:8192,http://151.145.76.6:8192"

// SetCaptchaRuntime updates process-wide captcha defaults used by SolveTurnstile*.
func SetCaptchaRuntime(c CaptchaRuntime) {
	captchaMu.Lock()
	defer captchaMu.Unlock()
	if c.DefaultProvider == "" {
		c.DefaultProvider = CaptchaProviderEzSolver
	}
	if strings.TrimSpace(c.EzSolver.URL) == "" {
		c.EzSolver.URL = DefaultEzSolverURL
	}
	if c.EzSolver.TimeoutMS <= 0 {
		c.EzSolver.TimeoutMS = 120000
	}
	if c.EzSolver.MaxConcurrency < 1 {
		c.EzSolver.MaxConcurrency = 1
	}
	// No upper clamp — follow admin "Max concurrency" / ezsolver_max_concurrency exactly.
	// No soft upper clamp anywhere — only floor at 1.
	if c.CaptchaRetry <= 0 {
		c.CaptchaRetry = 5
	}
	if strings.TrimSpace(c.Auralith.URL) == "" {
		c.Auralith.URL = DefaultAuralithURL
	}
	if c.Auralith.TimeoutMS <= 0 {
		c.Auralith.TimeoutMS = 120000
	}
	if c.Auralith.MaxConcurrency < 1 {
		c.Auralith.MaxConcurrency = 1
	}
	if c.AuralithRetry < 0 {
		c.AuralithRetry = 0
	}
	captchaRuntime = c
}

// captchaSolveAttemptLimitFor returns the total solve-attempt budget. The
// Auralith setting is explicitly a retry count, so its initial solve is added.
// The legacy EzSolver setting already represents the total attempt limit.
func captchaSolveAttemptLimitFor(provider CaptchaProvider) int {
	captchaMu.RLock()
	defer captchaMu.RUnlock()
	if provider == CaptchaProviderAuralith {
		retries := captchaRuntime.AuralithRetry
		if retries < 0 {
			retries = 0
		}
		return retries + 1
	}
	if captchaRuntime.CaptchaRetry < 1 {
		return 5
	}
	return captchaRuntime.CaptchaRetry
}

func captchaRetryCount() int {
	captchaMu.RLock()
	defer captchaMu.RUnlock()
	if captchaRuntime.CaptchaRetry < 1 {
		return 5
	}
	return captchaRuntime.CaptchaRetry
}

// GetCaptchaRuntime returns a copy of current captcha settings.
func GetCaptchaRuntime() CaptchaRuntime {
	captchaMu.RLock()
	defer captchaMu.RUnlock()
	return captchaRuntime
}

// solveViaProxyEnabled reports whether the Turnstile solve should egress through
// the registration's residential proxy (Plan A). Off by default.
func solveViaProxyEnabled() bool {
	captchaMu.RLock()
	defer captchaMu.RUnlock()
	return captchaRuntime.SolveViaProxy
}

func loadCaptchaSettings() CaptchaSettings {
	r := GetCaptchaRuntime()
	return CaptchaSettings{
		DefaultProvider: r.DefaultProvider,
		EzSolver:        r.EzSolver,
		Auralith:        r.Auralith,
	}
}

// stubs used by ported captcha.go (keys from runtime, not Kiro DB)
func getYesCaptchaKey(_, _ string) string {
	return strings.TrimSpace(GetCaptchaRuntime().YesCaptchaKey)
}

func getNextCaptchaKey(_ string) string {
	return strings.TrimSpace(GetCaptchaRuntime().NextCaptchaKey)
}

func yescaptchaEnabledFor(_ string) bool {
	return GetCaptchaRuntime().YesEnabled
}

// dbGetSettings was Kiro bbolt; unused after loadCaptchaSettings rewrite — keep nil-safe stub.
func dbGetSettings(_ string) map[string]any {
	return nil
}
