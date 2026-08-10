package grokregister

import (
	"context"
	"fmt"
	captchapkg "github.com/umbraforge/desktop/internal/pkg/captcha"
	"strings"
	"sync"
	"time"
)

type auralithGate struct {
	sem chan struct{}
	max int
}

var auralithGateMu sync.Mutex
var auralithGates = map[string]*auralithGate{}

func auralithNode(cfg AuralithConfig, opts CaptchaOptions) (string, string) {
	for _, n := range cfg.Nodes {
		if n.Enabled && strings.TrimSpace(n.URL) != "" {
			key := strings.TrimSpace(n.APIKey)
			if key == "" {
				key = strings.TrimSpace(opts.AuralithAPIKey)
			}
			if key == "" {
				key = strings.TrimSpace(cfg.APIKey)
			}
			return strings.TrimRight(strings.TrimSpace(n.URL), "/"), key
		}
	}
	u := strings.TrimRight(strings.TrimSpace(opts.AuralithURL), "/")
	if u == "" {
		u = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	}
	if u == "" {
		u = DefaultAuralithURL
	}
	k := strings.TrimSpace(opts.AuralithAPIKey)
	if k == "" {
		k = strings.TrimSpace(cfg.APIKey)
	}
	return u, k
}
func acquireAuralith(url string, max int, cancel *SafeCancel) (func(), error) {
	if max < 1 {
		max = 1
	}
	auralithGateMu.Lock()
	g := auralithGates[url]
	if g == nil || g.max != max {
		g = &auralithGate{sem: make(chan struct{}, max), max: max}
		auralithGates[url] = g
	}
	auralithGateMu.Unlock()
	if cancel == nil {
		g.sem <- struct{}{}
		return func() { <-g.sem }, nil
	}
	select {
	case g.sem <- struct{}{}:
		return func() { <-g.sem }, nil
	case <-cancel.Done():
		return nil, fmt.Errorf("cancelled")
	}
}

func rewriteAuralithProxyURL(proxyURL string, rewrites map[string]string) (string, bool) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" || len(rewrites) == 0 {
		return proxyURL, false
	}
	target, ok := rewrites[proxyURL]
	if !ok {
		return proxyURL, false
	}
	return strings.TrimSpace(target), true
}

func solveTurnstileAuralith(cfg AuralithConfig, opts CaptchaOptions, siteURL, siteKey string, cancel *SafeCancel) (*TurnstileSolveResult, error) {
	if isCancelled(cancel) {
		return nil, fmt.Errorf("cancelled")
	}
	url, key := auralithNode(cfg, opts)
	release, err := acquireAuralith(url, cfg.MaxConcurrency, cancel)
	if err != nil {
		return nil, err
	}
	defer release()
	timeout := cfg.TimeoutMS
	if timeout <= 0 {
		timeout = 120000
	}
	ctx, cancelHTTP := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
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
	client, err := captchapkg.NewAuralithClientValidated(url, max(1, timeout/1000), key)
	if err != nil {
		return nil, fmt.Errorf("Auralith URL 无效: %w", err)
	}
	// Align with VeloraTurn/EzSolver: only forward residential proxy when
	// captcha_proxy_mode enables SolveViaProxy. Batch still sets ProxyURL via
	// captchaProxyForWorker; this gate prevents accidental inherit when explicit
	// direct mode is selected.
	proxyForSolve := ""
	if solveViaProxyEnabled() {
		proxyForSolve = strings.TrimSpace(opts.ProxyURL)
	}
	proxyForSolve, proxyRewritten := rewriteAuralithProxyURL(proxyForSolve, cfg.ProxyRewrite)
	Logf("[Captcha] [Auralith] POST %s/solve site=%s key=%s... (timeout=%dms proxy=%v proxy_rewritten=%v)",
		url, siteURL, truncateStr(siteKey, 12), timeout, proxyForSolve != "", proxyRewritten)
	tok, err := client.Solve(ctx, siteKey, siteURL, proxyForSolve)
	if err != nil {
		return nil, err
	}
	Logf("[Captcha] [Auralith] 拿到 turnstile_token len=%d host=%s proxy=%v", len(tok), url, proxyForSolve != "")
	return &TurnstileSolveResult{Token: tok, Provider: CaptchaProviderAuralith}, nil
}
