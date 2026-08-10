package grokregister

import (
	"math"
	"math/rand"
	"net/url"
	"time"
)

// Standard header values
const (
	AcceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
	AcceptJSON = "application/json, text/plain, */*"
	AcceptAll  = "*/*"
	// Do NOT advertise zstd: the HTTP client does not reliably auto-decompress zstd
	// response bodies, so advertising it risks servers returning undecodable payloads
	// that break all downstream regex/JSON parsing (guarded by TestAcceptEncodingNoZstd,
	// and browser_headers.go is a changelog-protected file — see fingerprint-dev-changelog.mdc).
	AcceptEncoding    = "gzip, deflate, br"
	AcceptLangEN      = "en-US,en;q=0.9"
	AcceptLangZH      = "zh-CN,zh;q=0.9,en;q=0.8"
	ContentTypeJSON   = "application/json"
	ContentTypeForm   = "application/x-www-form-urlencoded"
	SecFetchDestDoc   = "document"
	SecFetchDestEmpty = "empty"
	SecFetchModeNav   = "navigate"
	SecFetchModeCors  = "cors"
	SecFetchSiteNone  = "none"
	SecFetchSiteSame  = "same-origin"
	SecFetchSiteCross = "cross-site"
)

// HeaderConfig allows customization of header generation.
type HeaderConfig struct {
	AcceptLanguage string         // Override Accept-Language (default: en-US,en;q=0.9)
	Cookie         string         // Cookie header value
	ExtraHeaders   OrderedHeaders // Additional headers to append
}

// NavigateHeaders returns ordered headers for a document navigation request (page load).
// This mimics a real browser's initial page request.
func NavigateHeaders(profile *BrowserProfile, targetURL string, cfg *HeaderConfig) OrderedHeaders {
	if cfg == nil {
		cfg = &HeaderConfig{}
	}
	acceptLang := cfg.AcceptLanguage
	if acceptLang == "" {
		acceptLang = AcceptLangEN
	}

	headers := OrderedHeaders{}

	// Chrome header order for navigate requests
	if profile.IsChromiumBased() {
		headers = append(headers,
			[]string{"sec-ch-ua", profile.SecCHUA},
			[]string{"sec-ch-ua-mobile", "?0"},
			[]string{"sec-ch-ua-platform", `"` + profile.Platform + `"`},
			[]string{"Upgrade-Insecure-Requests", "1"},
			[]string{"User-Agent", profile.UA},
			[]string{"Accept", AcceptHTML},
			[]string{"Sec-Fetch-Site", SecFetchSiteNone},
			[]string{"Sec-Fetch-Mode", SecFetchModeNav},
			[]string{"Sec-Fetch-User", "?1"},
			[]string{"Sec-Fetch-Dest", SecFetchDestDoc},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Accept-Language", acceptLang},
		)
	} else {
		// Firefox/Safari header order
		headers = append(headers,
			[]string{"User-Agent", profile.UA},
			[]string{"Accept", AcceptHTML},
			[]string{"Accept-Language", acceptLang},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Upgrade-Insecure-Requests", "1"},
			[]string{"Sec-Fetch-Dest", SecFetchDestDoc},
			[]string{"Sec-Fetch-Mode", SecFetchModeNav},
			[]string{"Sec-Fetch-Site", SecFetchSiteNone},
			[]string{"Sec-Fetch-User", "?1"},
		)
	}

	if cfg.Cookie != "" {
		headers.Set("Cookie", cfg.Cookie)
	}

	for _, h := range cfg.ExtraHeaders {
		if len(h) >= 2 {
			headers.Set(h[0], h[1])
		}
	}

	return headers
}

// XHRHeaders returns ordered headers for XHR/fetch API requests.
// fetchSite: "same-origin", "same-site", or "cross-site"
func XHRHeaders(profile *BrowserProfile, origin, referer, fetchSite string, cfg *HeaderConfig) OrderedHeaders {
	if cfg == nil {
		cfg = &HeaderConfig{}
	}
	acceptLang := cfg.AcceptLanguage
	if acceptLang == "" {
		acceptLang = AcceptLangEN
	}
	if fetchSite == "" {
		fetchSite = SecFetchSiteSame
	}

	headers := OrderedHeaders{}

	// Chrome header order for XHR/fetch requests
	if profile.IsChromiumBased() {
		headers = append(headers,
			[]string{"sec-ch-ua", profile.SecCHUA},
			[]string{"Accept", AcceptJSON},
			[]string{"sec-ch-ua-mobile", "?0"},
			[]string{"User-Agent", profile.UA},
			[]string{"sec-ch-ua-platform", `"` + profile.Platform + `"`},
			[]string{"Sec-Fetch-Site", fetchSite},
			[]string{"Sec-Fetch-Mode", SecFetchModeCors},
			[]string{"Sec-Fetch-Dest", SecFetchDestEmpty},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Accept-Language", acceptLang},
		)
	} else {
		// Firefox/Safari header order
		headers = append(headers,
			[]string{"User-Agent", profile.UA},
			[]string{"Accept", AcceptJSON},
			[]string{"Accept-Language", acceptLang},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Sec-Fetch-Dest", SecFetchDestEmpty},
			[]string{"Sec-Fetch-Mode", SecFetchModeCors},
			[]string{"Sec-Fetch-Site", fetchSite},
		)
	}

	if referer != "" {
		headers.Set("Referer", referer)
	}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	if cfg.Cookie != "" {
		headers.Set("Cookie", cfg.Cookie)
	}

	for _, h := range cfg.ExtraHeaders {
		if len(h) >= 2 {
			headers.Set(h[0], h[1])
		}
	}

	return headers
}

// ScriptHeaders returns ordered headers for a sub-resource script GET
// (e.g. Next.js /_next/static/chunks/*.js). Real Chrome sends these with
// Sec-Fetch-Dest=script / Mode=no-cors, NOT the document-navigation metadata —
// reusing navigation headers for scripts is an obvious coherence tell.
func ScriptHeaders(profile *BrowserProfile, targetURL, referer string, cfg *HeaderConfig) OrderedHeaders {
	if cfg == nil {
		cfg = &HeaderConfig{}
	}
	acceptLang := cfg.AcceptLanguage
	if acceptLang == "" {
		acceptLang = AcceptLangEN
	}
	fetchSite := SecFetchSiteSame
	if referer != "" {
		fetchSite = determineFetchSite(referer, targetURL)
	}

	headers := OrderedHeaders{}
	if profile.IsChromiumBased() {
		headers = append(headers,
			[]string{"sec-ch-ua", profile.SecCHUA},
			[]string{"sec-ch-ua-mobile", "?0"},
			[]string{"User-Agent", profile.UA},
			[]string{"sec-ch-ua-platform", `"` + profile.Platform + `"`},
			[]string{"Accept", AcceptAll},
			[]string{"Sec-Fetch-Site", fetchSite},
			[]string{"Sec-Fetch-Mode", "no-cors"},
			[]string{"Sec-Fetch-Dest", "script"},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Accept-Language", acceptLang},
		)
	} else {
		headers = append(headers,
			[]string{"User-Agent", profile.UA},
			[]string{"Accept", AcceptAll},
			[]string{"Accept-Language", acceptLang},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Sec-Fetch-Dest", "script"},
			[]string{"Sec-Fetch-Mode", "no-cors"},
			[]string{"Sec-Fetch-Site", fetchSite},
		)
	}
	if referer != "" {
		headers.Set("Referer", referer)
	}
	if cfg.Cookie != "" {
		headers.Set("Cookie", cfg.Cookie)
	}
	for _, h := range cfg.ExtraHeaders {
		if len(h) >= 2 {
			headers.Set(h[0], h[1])
		}
	}
	return headers
}

// RedirectNavHeaders returns ordered headers for following a top-level navigation
// redirect (e.g. the post-submit set-cookie → SSO chain). A real browser navigation
// always carries the same Client Hints, Fetch Metadata and Accept-* as the initial
// page load — sending only User-Agent + Accept here was an incoherence tell.
//
// prevURL is the URL that produced this hop (initiating page for the first hop,
// previous Location for subsequent hops); it is used ONLY to compute Sec-Fetch-Site.
// No Referer is set: server redirects usually drop it, and a wrong Referer is worse
// than none. Sec-Fetch-User is intentionally omitted — redirects are not user-activated.
func RedirectNavHeaders(profile *BrowserProfile, targetURL, prevURL string, cfg *HeaderConfig) OrderedHeaders {
	if cfg == nil {
		cfg = &HeaderConfig{}
	}
	acceptLang := cfg.AcceptLanguage
	if acceptLang == "" {
		acceptLang = AcceptLangEN
	}
	fetchSite := SecFetchSiteNone
	if prevURL != "" {
		fetchSite = determineFetchSite(prevURL, targetURL)
	}

	headers := OrderedHeaders{}
	if profile.IsChromiumBased() {
		headers = append(headers,
			[]string{"sec-ch-ua", profile.SecCHUA},
			[]string{"sec-ch-ua-mobile", "?0"},
			[]string{"sec-ch-ua-platform", `"` + profile.Platform + `"`},
			[]string{"Upgrade-Insecure-Requests", "1"},
			[]string{"User-Agent", profile.UA},
			[]string{"Accept", AcceptHTML},
			[]string{"Sec-Fetch-Site", fetchSite},
			[]string{"Sec-Fetch-Mode", SecFetchModeNav},
			[]string{"Sec-Fetch-Dest", SecFetchDestDoc},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Accept-Language", acceptLang},
		)
	} else {
		headers = append(headers,
			[]string{"User-Agent", profile.UA},
			[]string{"Accept", AcceptHTML},
			[]string{"Accept-Language", acceptLang},
			[]string{"Accept-Encoding", AcceptEncoding},
			[]string{"Upgrade-Insecure-Requests", "1"},
			[]string{"Sec-Fetch-Dest", SecFetchDestDoc},
			[]string{"Sec-Fetch-Mode", SecFetchModeNav},
			[]string{"Sec-Fetch-Site", fetchSite},
		)
	}

	if cfg.Cookie != "" {
		headers.Set("Cookie", cfg.Cookie)
	}
	for _, h := range cfg.ExtraHeaders {
		if len(h) >= 2 {
			headers.Set(h[0], h[1])
		}
	}
	return headers
}

// JSONPostHeaders returns ordered headers for JSON POST requests.
func JSONPostHeaders(profile *BrowserProfile, origin, referer, fetchSite string, cfg *HeaderConfig) OrderedHeaders {
	headers := XHRHeaders(profile, origin, referer, fetchSite, cfg)
	headers.Set("Content-Type", ContentTypeJSON)
	return headers
}

// FormPostHeaders returns ordered headers for form POST requests.
func FormPostHeaders(profile *BrowserProfile, origin, referer, fetchSite string, cfg *HeaderConfig) OrderedHeaders {
	headers := XHRHeaders(profile, origin, referer, fetchSite, cfg)
	headers.Set("Content-Type", ContentTypeForm)
	return headers
}

// APIHeaders returns minimal ordered headers for API requests that may not need full browser simulation.
// Includes User-Agent and Accept but fewer fingerprint-sensitive headers.
func APIHeaders(profile *BrowserProfile, cfg *HeaderConfig) OrderedHeaders {
	if cfg == nil {
		cfg = &HeaderConfig{}
	}
	acceptLang := cfg.AcceptLanguage
	if acceptLang == "" {
		acceptLang = AcceptLangEN
	}

	headers := OrderedHeaders{
		{"Accept", AcceptJSON},
		{"Accept-Language", acceptLang},
		{"Accept-Encoding", AcceptEncoding},
		{"User-Agent", profile.UA},
	}

	if profile.IsChromiumBased() {
		headers = append(headers,
			[]string{"sec-ch-ua", profile.SecCHUA},
			[]string{"sec-ch-ua-mobile", "?0"},
			[]string{"sec-ch-ua-platform", `"` + profile.Platform + `"`},
		)
	}

	if cfg.Cookie != "" {
		headers.Set("Cookie", cfg.Cookie)
	}

	for _, h := range cfg.ExtraHeaders {
		if len(h) >= 2 {
			headers.Set(h[0], h[1])
		}
	}

	return headers
}

// determineFetchSite determines the Sec-Fetch-Site value based on origin and target URLs.
func determineFetchSite(originURL, targetURL string) string {
	if originURL == "" {
		return SecFetchSiteNone
	}

	origin, err1 := url.Parse(originURL)
	target, err2 := url.Parse(targetURL)
	if err1 != nil || err2 != nil {
		return SecFetchSiteCross
	}

	if origin.Host == target.Host {
		return SecFetchSiteSame
	}

	// Check for same-site (same eTLD+1)
	originParts := splitHost(origin.Host)
	targetParts := splitHost(target.Host)

	if len(originParts) >= 2 && len(targetParts) >= 2 {
		originSite := originParts[len(originParts)-2] + "." + originParts[len(originParts)-1]
		targetSite := targetParts[len(targetParts)-2] + "." + targetParts[len(targetParts)-1]
		if originSite == targetSite {
			return "same-site"
		}
	}

	return SecFetchSiteCross
}

func splitHost(host string) []string {
	// Remove port if present
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			host = host[:i]
			break
		}
		if host[i] < '0' || host[i] > '9' {
			break
		}
	}
	var parts []string
	start := 0
	for i := 0; i <= len(host); i++ {
		if i == len(host) || host[i] == '.' {
			if i > start {
				parts = append(parts, host[start:i])
			}
			start = i + 1
		}
	}
	return parts
}

// HumanDelay generates a human-like delay using normal distribution.
// mean: average delay in milliseconds
// stddev: standard deviation in milliseconds
// minMs/maxMs: clamp bounds
func HumanDelay(meanMs, stddevMs, minMs, maxMs float64) time.Duration {
	// Box-Muller transform for normal distribution
	// Ensure u1 > 0 to avoid math.Log(0) = -Inf
	u1 := rand.Float64()
	for u1 == 0 {
		u1 = rand.Float64()
	}
	u2 := rand.Float64()
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	delay := meanMs + z*stddevMs

	// Clamp to bounds
	if delay < minMs {
		delay = minMs
	}
	if delay > maxMs {
		delay = maxMs
	}

	return time.Duration(delay) * time.Millisecond
}

// PageLoadDelay returns a human-like delay after page loads (1-3 seconds, mean 2s)
func PageLoadDelay() time.Duration {
	return HumanDelay(2000, 500, 1000, 3500)
}

// FormFillDelay returns a human-like delay for form filling (0.5-2 seconds, mean 1s)
func FormFillDelay() time.Duration {
	return HumanDelay(1000, 300, 500, 2000)
}

// SubmitDelay returns a human-like delay before form submission (1-2 seconds, mean 1.5s)
func SubmitDelay() time.Duration {
	return HumanDelay(1500, 400, 800, 2500)
}

// TypingDelay returns a human-like delay simulating reading/typing (2-5 seconds, mean 3s)
func TypingDelay() time.Duration {
	return HumanDelay(3000, 800, 1500, 5000)
}

// humanDelayEnabled controls MaybeDelay / ShouldHumanDelay (default on).
// Set via SetHumanDelayEnabled from batch options — Kiro used loadBatchConfig().
var humanDelayEnabled = true

// SetHumanDelayEnabled toggles human-like delays for the register package.
func SetHumanDelayEnabled(v bool) {
	humanDelayEnabled = v
}

// ShouldHumanDelay reports whether human-like delays are enabled.
func ShouldHumanDelay() bool {
	return humanDelayEnabled
}

// MaybeDelay sleeps for the given duration only if HumanDelay is enabled.
func MaybeDelay(delayFn func() time.Duration) {
	if ShouldHumanDelay() {
		time.Sleep(delayFn())
	}
}
