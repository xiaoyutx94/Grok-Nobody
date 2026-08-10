package grokregister

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Grok (x.ai) Registration ──

const (
	grokSiteURL    = "https://accounts.x.ai"
	grokSignUpURL  = "https://accounts.x.ai/sign-up"
	grokSiteKey    = "0x4AAAAAAAhr9JGVDZbrZOo0"
	grokStateTree  = "%5B%22%22%2C%7B%22children%22%3A%5B%22(app)%22%2C%7B%22children%22%3A%5B%22(auth)%22%2C%7B%22children%22%3A%5B%22sign-up%22%2C%7B%22children%22%3A%5B%22__PAGE__%22%2C%7B%7D%2C%22%2Fsign-up%22%2C%22refresh%22%5D%7D%5D%7D%2Cnull%2Cnull%5D%7D%2Cnull%2Cnull%5D%7D%2Cnull%2Cnull%2Ctrue%5D"
	grokGRPCCreate = "/auth_mgmt.AuthManagement/CreateEmailValidationCode"
	grokGRPCVerify = "/auth_mgmt.AuthManagement/VerifyEmailValidationCode"
)

// grokConfig holds dynamic config extracted from the sign-up page
type grokConfig struct {
	SiteKey   string
	StateTree string
	ActionID  string
}

// Process-wide cache for ActionID / sitekey / router state.
// Same x.ai build usually keeps these stable for hours; re-fetching every account
// burns multi-second JS downloads under concurrency.
var (
	grokCfgCacheMu  sync.RWMutex
	grokCfgLoadMu   sync.Mutex
	grokCfgCache    *grokConfig
	grokCfgCacheAt  time.Time
	grokCfgCacheTTL = 20 * time.Minute
)

func grokCachedConfig() *grokConfig {
	grokCfgCacheMu.RLock()
	defer grokCfgCacheMu.RUnlock()
	if grokCfgCache == nil || grokCfgCache.ActionID == "" {
		return nil
	}
	if time.Since(grokCfgCacheAt) > grokCfgCacheTTL {
		return nil
	}
	// Return a copy so callers cannot mutate the shared cache.
	cp := *grokCfgCache
	return &cp
}

func grokStoreConfigCache(cfg *grokConfig) {
	if cfg == nil || cfg.ActionID == "" {
		return
	}
	cp := *cfg
	grokCfgCacheMu.Lock()
	grokCfgCache = &cp
	grokCfgCacheAt = time.Now()
	grokCfgCacheMu.Unlock()
}

func grokInvalidateConfigCache() {
	grokCfgCacheMu.Lock()
	grokCfgCache = nil
	grokCfgCacheAt = time.Time{}
	grokCfgCacheMu.Unlock()
}

// grokPhase tracks per-step wall time for batch diagnostics.
type grokPhase struct {
	name string
	t0   time.Time
}

func grokStartPhase(name string) grokPhase {
	return grokPhase{name: name, t0: time.Now()}
}

func (p grokPhase) end() float64 {
	return time.Since(p.t0).Seconds()
}

func (p grokPhase) logDone(extra string) float64 {
	sec := p.end()
	if extra != "" {
		Logf("[Grok] [耗时] %s: %.1fs | %s", p.name, sec, extra)
	} else {
		Logf("[Grok] [耗时] %s: %.1fs", p.name, sec)
	}
	return sec
}

type GrokRegistrar struct {
	client        *HTTPClient
	cancel        *SafeCancel
	proxy         string
	profile       *BrowserProfile
	emailMode     string
	emailSvc      EmailService
	stepDelay     int
	otpPoll       time.Duration
	skipVerify    bool // true = 跳过 Step 9 验活
	onOTPVerified func(email string)
}

// SetOnOTPVerified 注册验证码通过验证后的回调（iCloud 用完即删）。
func (r *GrokRegistrar) SetOnOTPVerified(fn func(email string)) {
	r.onOTPVerified = fn
}

func NewGrokRegistrar(proxy string, cancel *SafeCancel, profile *BrowserProfile) *GrokRegistrar {
	proxyOn := proxy != ""
	browserKey := ""
	if profile != nil {
		browserKey = profile.Browser
	}
	return &GrokRegistrar{
		client:  NewHTTPClientWithBrowser(proxy, proxyOn, browserKey),
		cancel:  cancel,
		proxy:   proxy,
		profile: profile,
	}
}

func (r *GrokRegistrar) isCancelled() bool {
	return isCancelled(r.cancel)
}

// ── gRPC-Web protobuf encoding ──

// encodeVarint encodes an integer as a protobuf varint (supports values > 127)
func encodeVarint(n int) []byte {
	var buf []byte
	for n >= 0x80 {
		buf = append(buf, byte(n)|0x80)
		n >>= 7
	}
	buf = append(buf, byte(n))
	return buf
}

// encodeProtobufField encodes a protobuf length-delimited field (wire type 2)
func encodeProtobufField(fieldID int, value []byte) []byte {
	tag := encodeVarint((fieldID << 3) | 2)
	length := encodeVarint(len(value))
	result := make([]byte, 0, len(tag)+len(length)+len(value))
	result = append(result, tag...)
	result = append(result, length...)
	result = append(result, value...)
	return result
}

// grokGRPCFrame wraps a protobuf payload in a gRPC-Web frame (0x00 + 4-byte big-endian length + payload)
func grokGRPCFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = 0x00
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

// grokEncodeGRPC encodes a single string field for gRPC-Web
func grokEncodeGRPC(fieldID int, value string) []byte {
	payload := encodeProtobufField(fieldID, []byte(value))
	return grokGRPCFrame(payload)
}

// grokEncodeGRPCVerify encodes email (field 1) + code (field 2) for gRPC-Web
func grokEncodeGRPCVerify(email, code string) []byte {
	p1 := encodeProtobufField(1, []byte(email))
	p2 := encodeProtobufField(2, []byte(code))
	payload := append(p1, p2...)
	return grokGRPCFrame(payload)
}

// ── Turnstile CAPTCHA solving ──
// Legacy thin wrapper: prefer SolveTurnstileWithOptions / CaptchaOptions.

func grokSolveTurnstile(apiKey, siteKey string, cancel *SafeCancel) (string, error) {
	res, err := SolveTurnstileWithOptions(CaptchaOptions{
		Provider:      CaptchaProviderYesCaptcha,
		YesCaptchaKey: apiKey,
		SiteURL:       grokSiteURL,
		SiteKey:       siteKey,
	}, cancel)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", fmt.Errorf("Turnstile 结果为空")
	}
	return res.Token, nil
}

// ── Dynamic config extraction ──

var (
	grokActionIDRegex = regexp.MustCompile(`7f[a-fA-F0-9]{40}`)
	// Fallback for when x.ai rotates its build and the sign-up server-action ID no longer
	// starts with "7f". Next.js registers server actions in the client bundle via
	// createServerReference("<40-hex-id>", ...); this is the canonical, prefix-agnostic form.
	grokActionIDCSRRegex = regexp.MustCompile(`createServerReference\(\s*"([0-9a-fA-F]{40})"`)
	grokSiteKeyRegex     = regexp.MustCompile(`sitekey":"(0x4[a-zA-Z0-9_-]+)"`)
	grokStateRegex       = regexp.MustCompile(`next-router-state-tree":"([^"]+)"`)
	grokJSURLRegex       = regexp.MustCompile(`/_next/static/chunks/[^"'\s>]+\.js`)
	grokCodeRegex        = regexp.MustCompile(`>([A-Z0-9]{3}-[A-Z0-9]{3})<`)
	grokSetCookieRegex   = regexp.MustCompile(`(https://[^"\s]+set-cookie\?q=[^:"\s]+)1:`)
	// Limit concurrent Next-Action POSTs (not fully serial). Full mutex was killing
	// multi-worker speed: 5 workers still submitted one-by-one ~1–2s each + queue.
	// Cap at 12: enough for concurrency=10 without long submit queues.
	grokPostSem = make(chan struct{}, 12)
)

// grokGRPCTrailerError inspects a gRPC-Web response body for a non-zero grpc-status
// trailer. gRPC-Web returns errors as HTTP 200 with the status in a trailer frame, so
// checking only the HTTP status code would treat a rejected/rate-limited request as a
// success. Returns "" when the call succeeded (status 0 or no trailer present).
func grokGRPCTrailerError(body []byte) string {
	s := string(body)
	idx := strings.Index(s, "grpc-status:")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[idx+len("grpc-status:"):])
	statusVal := rest
	if end := strings.IndexAny(rest, "\r\n "); end >= 0 {
		statusVal = rest[:end]
	}
	if statusVal == "" || statusVal == "0" {
		return ""
	}
	msg := ""
	if mi := strings.Index(s, "grpc-message:"); mi >= 0 {
		mr := strings.TrimSpace(s[mi+len("grpc-message:"):])
		if me := strings.IndexAny(mr, "\r\n"); me >= 0 {
			msg = mr[:me]
		} else {
			msg = mr
		}
	}
	return fmt.Sprintf("grpc-status=%s %s", statusVal, strings.TrimSpace(msg))
}

func grokInitConfig(client *HTTPClient, profile *BrowserProfile) (*grokConfig, error) {
	cfg := &grokConfig{
		SiteKey:   grokSiteKey,
		StateTree: grokStateTree,
	}

	// Document navigation to the sign-up page: full real-Chrome navigation headers
	// (sec-ch-ua*, Sec-Fetch document metadata) so the UA claim and the sent Client
	// Hints are coherent. JS chunks below use script headers instead (different Dest).
	headers := NavigateHeaders(profile, grokSignUpURL, nil)

	// Prefer cached ActionID (build-stable). Still GET /sign-up once for cookies + live sitekey/state.
	cached := grokCachedConfig()
	if cached != nil {
		cfg.ActionID = cached.ActionID
		if cached.SiteKey != "" {
			cfg.SiteKey = cached.SiteKey
		}
		if cached.StateTree != "" {
			cfg.StateTree = cached.StateTree
		}
		Logf("[Grok] Action ID 使用缓存: %s (age<%s)", cfg.ActionID, grokCfgCacheTTL)
	}

	// High concurrency + residential SOCKS often yields transient EOF/reset on first hop.
	// Retry a few times on the same client/proxy before failing the whole task.
	var resp *Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = client.GetOrdered(grokSignUpURL, headers)
		if err == nil {
			break
		}
		errText := strings.ToLower(err.Error())
		// Auth failures on SOCKS won't recover on the same proxy — fail fast for batch retry/switch.
		if strings.Contains(errText, "authentication failed") ||
			strings.Contains(errText, "username/password") ||
			strings.Contains(errText, "auth failed") {
			break
		}
		transient := strings.Contains(errText, "eof") ||
			strings.Contains(errText, "reset") ||
			strings.Contains(errText, "broken pipe") ||
			strings.Contains(errText, "connection refused") ||
			strings.Contains(errText, "i/o timeout") ||
			strings.Contains(errText, "timeout") ||
			strings.Contains(errText, "tls") ||
			strings.Contains(errText, "socks")
		if !transient || attempt == 2 {
			break
		}
		Logf("[Grok] 访问注册页瞬时失败 (尝试 %d/3): %v — 同代理重试", attempt+1, err)
		time.Sleep(time.Duration(400*(attempt+1)) * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("访问注册页面失败: %v", err)
	}
	client.SaveCookies(resp)
	html := string(resp.Body)

	// Extract site_key / state_tree from live HTML when present.
	if m := grokSiteKeyRegex.FindStringSubmatch(html); len(m) >= 2 {
		cfg.SiteKey = m[1]
	}
	if m := grokStateRegex.FindStringSubmatch(html); len(m) >= 2 {
		cfg.StateTree = m[1]
	}

	// Hot path: cookies + ActionID cache → skip JS chunk fan-out.
	if cfg.ActionID != "" {
		grokStoreConfigCache(cfg)
		return cfg, nil
	}

	// Only one cold worker scans Next.js chunks. Other workers already fetched the
	// sign-up page for their own cookies and can reuse the discovered action ID.
	grokCfgLoadMu.Lock()
	defer grokCfgLoadMu.Unlock()
	if cached := grokCachedConfig(); cached != nil {
		cfg.ActionID = cached.ActionID
		if cached.SiteKey != "" {
			cfg.SiteKey = cached.SiteKey
		}
		if cached.StateTree != "" {
			cfg.StateTree = cached.StateTree
		}
		Logf("[Grok] Action ID 使用并发冷启动结果: %s", cfg.ActionID)
		return cfg, nil
	}

	// Cold path: scan JS chunks for Action ID.
	rawJS := grokJSURLRegex.FindAllString(html, -1)

	// Cloudflare interstitial (not the real sign-up SPA). Real page embeds Turnstile and may
	// mention challenge-platform — only treat as block when Next.js chunks are missing.
	titleCF := strings.Contains(html, "Attention Required") || strings.Contains(html, "Just a moment")
	httpBad := resp.StatusCode == 403 || (resp.StatusCode > 0 && (resp.StatusCode < 200 || resp.StatusCode >= 400))
	if len(rawJS) == 0 && (httpBad || titleCF || len(html) < 15000) {
		Logf("[Grok] 注册页被 Cloudflare/空壳拦截 HTTP=%d len=%d titleCF=%v — 同代理再试一次", resp.StatusCode, len(html), titleCF)
		time.Sleep(600 * time.Millisecond)
		resp2, err2 := client.GetOrdered(grokSignUpURL, headers)
		if err2 == nil && resp2 != nil {
			client.SaveCookies(resp2)
			html = string(resp2.Body)
			resp = resp2
			rawJS = grokJSURLRegex.FindAllString(html, -1)
			titleCF = strings.Contains(html, "Attention Required") || strings.Contains(html, "Just a moment")
			httpBad = resp.StatusCode == 403 || (resp.StatusCode > 0 && (resp.StatusCode < 200 || resp.StatusCode >= 400))
			if m := grokSiteKeyRegex.FindStringSubmatch(html); len(m) >= 2 {
				cfg.SiteKey = m[1]
			}
			if m := grokStateRegex.FindStringSubmatch(html); len(m) >= 2 {
				cfg.StateTree = m[1]
			}
		}
		if len(rawJS) == 0 && (httpBad || titleCF || len(html) < 15000) {
			snippet := html
			if len(snippet) > 180 {
				snippet = snippet[:180]
			}
			Logf("[Grok] HTML 片段: %s", strings.ReplaceAll(snippet, "\n", " "))
			if httpBad {
				return nil, fmt.Errorf("注册页被 Cloudflare 拦截(HTTP %d)，请更换住宅代理后重试", resp.StatusCode)
			}
			return nil, fmt.Errorf("注册页无 Next.js 资源(len=%d)，疑似 Cloudflare 挑战，请更换住宅代理后重试", len(html))
		}
	}
	if len(rawJS) == 0 {
		Logf("[Grok] 警告: HTML 长度 %d HTTP=%d, 未解析出 JS chunks", len(html), resp.StatusCode)
	}

	// Deduplicate while preserving first-seen order. RSC payload repeats the same chunk
	// path hundreds of times — without dedupe, "top-24" only covers ~3 unique files.
	seenJS := make(map[string]struct{}, len(rawJS))
	jsMatches := make([]string, 0, 64)
	for _, p := range rawJS {
		if _, ok := seenJS[p]; ok {
			continue
		}
		seenJS[p] = struct{}{}
		jsMatches = append(jsMatches, p)
	}

	// Prefer likely server-action chunks first. x.ai turbopack names are mostly opaque.
	sort.SliceStable(jsMatches, func(i, j int) bool {
		score := func(p string) int {
			lp := strings.ToLower(p)
			s := 0
			for _, k := range []string{"action", "server", "auth", "sign", "page", "main", "app", "signup", "register"} {
				if strings.Contains(lp, k) {
					s += 2
				}
			}
			if strings.Contains(lp, "polyfill") || strings.Contains(lp, "webpack") || strings.Contains(lp, "turbopack") {
				s -= 3
			}
			return s
		}
		return score(jsMatches[i]) > score(jsMatches[j])
	})

	// Cap cold-path JS downloads. Unique chunks ~40; Action ID observed near index 36–40.
	// Hot path skips via ActionID cache (20m) after first success.
	const maxJSProbes = 48
	if len(jsMatches) > maxJSProbes {
		Logf("[Grok] JS unique %d (raw=%d) → 探测 top-%d（命中后缓存 20m）", len(jsMatches), len(rawJS), maxJSProbes)
		jsMatches = jsMatches[:maxJSProbes]
	} else if len(rawJS) != len(jsMatches) {
		Logf("[Grok] JS unique %d (raw=%d) — 全量探测", len(jsMatches), len(rawJS))
	}
	fallbackID := ""
	// JS chunks are script sub-resources, not document navigations — send them with
	// Sec-Fetch-Dest=script / Mode=no-cors (referer = the sign-up page).
	scriptHeaders := ScriptHeaders(profile, grokSiteURL, grokSignUpURL, nil)
	for _, jsPath := range jsMatches {
		jsURL := grokSiteURL + jsPath
		jsResp, err := client.GetOrdered(jsURL, scriptHeaders)
		if err != nil {
			continue
		}
		jsContent := string(jsResp.Body)
		if m := grokActionIDRegex.FindString(jsContent); m != "" {
			cfg.ActionID = m
			Logf("[Grok] Action ID: %s", cfg.ActionID)
			break
		}
		// Remember a createServerReference id in case the strict "7f..." form never matches
		// (x.ai rotated its build). Prefer the strict match; only fall back if it's absent.
		if fallbackID == "" {
			if m := grokActionIDCSRRegex.FindStringSubmatch(jsContent); len(m) >= 2 {
				fallbackID = m[1]
			}
		}
	}

	if cfg.ActionID == "" && fallbackID != "" {
		cfg.ActionID = fallbackID
		Logf("[Grok] Action ID (fallback via createServerReference): %s", cfg.ActionID)
	}

	if cfg.ActionID == "" {
		return nil, fmt.Errorf("未找到 Action ID，请更换代理或重试")
	}

	grokStoreConfigCache(cfg)
	return cfg, nil
}

// ── gRPC-Web requests ──

func (r *GrokRegistrar) grpcHeaders() OrderedHeaders {
	// connect-es gRPC-Web fetch from a real Chrome carries the same Client Hints and
	// Fetch Metadata as any same-origin CORS fetch, on top of the gRPC-Web specifics.
	// Sending the gRPC specifics WITHOUT sec-ch-ua while the UA claims Chrome was an
	// incoherence tell; weave the coherent hints in for Chromium profiles.
	h := OrderedHeaders{
		{"Content-Type", "application/grpc-web+proto"},
		{"X-Grpc-Web", "1"},
		{"X-User-Agent", "connect-es/2.1.1"},
	}
	if r.profile.IsChromiumBased() {
		h = append(h,
			[]string{"sec-ch-ua", r.profile.SecCHUA},
			[]string{"sec-ch-ua-mobile", "?0"},
			[]string{"sec-ch-ua-platform", `"` + r.profile.Platform + `"`},
		)
	}
	h = append(h,
		[]string{"User-Agent", r.profile.UA},
		[]string{"Accept", AcceptAll},
		[]string{"Sec-Fetch-Site", SecFetchSiteSame},
		[]string{"Sec-Fetch-Mode", SecFetchModeCors},
		[]string{"Sec-Fetch-Dest", SecFetchDestEmpty},
		[]string{"Accept-Encoding", AcceptEncoding},
		[]string{"Accept-Language", AcceptLangEN},
		[]string{"Origin", grokSiteURL},
		[]string{"Referer", grokSiteURL + "/sign-up?redirect=grok-com"},
	)
	return h
}

func (r *GrokRegistrar) sendEmailCode(email string) error {
	url := grokSiteURL + grokGRPCCreate
	data := grokEncodeGRPC(1, email)
	resp, err := r.client.PostRawOrdered(url, r.grpcHeaders(), data)
	if err != nil {
		return fmt.Errorf("发送验证码请求失败: %v", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("发送验证码返回 %d: %s", resp.StatusCode, truncStr(string(resp.Body), 200))
	}
	if gerr := grokGRPCTrailerError(resp.Body); gerr != "" {
		return fmt.Errorf("发送验证码被拒 (%s)", gerr)
	}
	return nil
}

func (r *GrokRegistrar) verifyEmailCode(email, code string) error {
	url := grokSiteURL + grokGRPCVerify
	data := grokEncodeGRPCVerify(email, code)
	resp, err := r.client.PostRawOrdered(url, r.grpcHeaders(), data)
	if err != nil {
		return fmt.Errorf("验证码验证请求失败: %v", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("验证码验证返回 %d: %s", resp.StatusCode, truncStr(string(resp.Body), 200))
	}
	if gerr := grokGRPCTrailerError(resp.Body); gerr != "" {
		return fmt.Errorf("验证码验证被拒 (%s)", gerr)
	}
	return nil
}

// ── Mail.tm code fetching (Grok-specific format: XXX-XXX) ──

func grokGetVerifyCode(client *HTTPClient, token, email string, interval time.Duration, cancel *SafeCancel) (string, error) {
	seenIDs := make(map[string]bool)
	Logf("[Grok] 正在等待邮箱 %s 的验证码...", email)
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		if isCancelled(cancel) {
			return "", fmt.Errorf("cancelled")
		}
		sleepWithCancel(interval, cancel)

		resp, err := client.Get(mailtmBase+"/messages", map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + token,
		})
		if err != nil || resp.StatusCode != 200 {
			continue
		}

		type msgItem struct {
			ID string `json:"id"`
		}
		var messages []msgItem
		if err := json.Unmarshal(resp.Body, &messages); err != nil {
			var obj struct {
				Member   []msgItem `json:"hydra:member"`
				Messages []msgItem `json:"messages"`
			}
			json.Unmarshal(resp.Body, &obj)
			if len(obj.Member) > 0 {
				messages = obj.Member
			} else {
				messages = obj.Messages
			}
		}

		for _, msg := range messages {
			if msg.ID == "" || seenIDs[msg.ID] {
				continue
			}
			seenIDs[msg.ID] = true

			msgResp, err := client.Get(mailtmBase+"/messages/"+msg.ID, map[string]string{
				"Accept":        "application/json",
				"Authorization": "Bearer " + token,
			})
			if err != nil || msgResp.StatusCode != 200 {
				continue
			}

			var mailData struct {
				Subject string          `json:"subject"`
				Intro   string          `json:"intro"`
				Text    string          `json:"text"`
				HTML    json.RawMessage `json:"html"`
			}
			if err := json.Unmarshal(msgResp.Body, &mailData); err != nil {
				continue
			}

			var htmlStr string
			var htmlArr []string
			if json.Unmarshal(mailData.HTML, &htmlArr) == nil {
				htmlStr = strings.Join(htmlArr, "\n")
			} else {
				var htmlSingle string
				if json.Unmarshal(mailData.HTML, &htmlSingle) == nil {
					htmlStr = htmlSingle
				} else {
					htmlStr = string(mailData.HTML)
				}
			}
			content := strings.Join([]string{mailData.Subject, mailData.Intro, mailData.Text, htmlStr}, "\n")

			// Grok verification code format: >XXX-XXX<
			m := grokCodeRegex.FindStringSubmatch(content)
			if len(m) >= 2 {
				code := strings.ReplaceAll(m[1], "-", "")
				Logf("[Grok] 抓到验证码: %s", code)
				return code, nil
			}
		}
	}
	return "", fmt.Errorf("等待验证码超时 (60s)")
}

// ── Random name generation ──

func grokRandomName() string {
	const upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const lower = "abcdefghijklmnopqrstuvwxyz"
	length := 4 + rand.Intn(3) // 4-6
	name := make([]byte, length)
	name[0] = upper[rand.Intn(len(upper))]
	for i := 1; i < length; i++ {
		name[i] = lower[rand.Intn(len(lower))]
	}
	return string(name)
}

func grokRandomPassword() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 15)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// grokCodePlainRegex matches Grok's XXX-XXX code in plain text (after HTML stripping).
var grokCodePlainRegex = regexp.MustCompile(`\b([A-Z0-9]{3}-[A-Z0-9]{3})\b`)

// grokExtractCode extracts Grok's XXX-XXX verification code from email text (returns code without dash).
// Supports both HTML (>XXX-XXX<) and plain text (XXX-XXX) formats.
func grokExtractCode(text string) string {
	// Try HTML format first (used by Mail.tm raw HTML)
	if m := grokCodeRegex.FindStringSubmatch(text); len(m) >= 2 {
		return strings.ReplaceAll(m[1], "-", "")
	}
	// Try plain text format (used by Outlook IMAP after stripHTML)
	if m := grokCodePlainRegex.FindStringSubmatch(text); len(m) >= 2 {
		return strings.ReplaceAll(m[1], "-", "")
	}
	return ""
}

// grokSubmitCookieHeader preserves the browser context collected while solving
// Turnstile. HTTPClient stores cookies without domain metadata, so every stored
// value belongs to the active registration session and is safe to forward here.
func grokSubmitCookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	keys := make([]string, 0, len(cookies))
	for name, value := range cookies {
		if strings.TrimSpace(name) != "" && value != "" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, name+"="+cookies[name])
	}
	return strings.Join(parts, "; ")
}

func captchaSolveAttempts(provider CaptchaProvider, err error) int {
	if provider == CaptchaProviderEzSolver && shouldRetryEzSolverError(err) {
		return 2
	}
	if provider == CaptchaProviderAuralith && captchaSolveAttemptLimitFor(provider) > 1 {
		return 2
	}
	return 1
}

// ── Main registration flow ──

func (r *GrokRegistrar) Run(captcha CaptchaOptions) map[string]interface{} {
	parentCancel := r.cancel
	jobCancel := NewChildCancel(parentCancel)
	r.cancel = jobCancel
	defer func() {
		jobCancel.Close()
		r.cancel = parentCancel
	}()

	result := map[string]interface{}{
		"email":  "",
		"status": "failed",
	}
	totalT0 := time.Now()
	phaseMS := map[string]float64{}
	logPhase := func(name string, sec float64, extra string) {
		phaseMS[name] = sec
		if extra != "" {
			Logf("[Grok] [耗时] %s: %.1fs | %s", name, sec, extra)
		} else {
			Logf("[Grok] [耗时] %s: %.1fs", name, sec)
		}
	}
	finishTiming := func() {
		total := time.Since(totalT0).Seconds()
		Logf("[Grok] [耗时] TOTAL: %.1fs | init=%.1f email=%.1f send=%.1f otp=%.1f verify=%.1f captcha=%.1f submit=%.1f oauth=%.1f",
			total,
			phaseMS["init"], phaseMS["email"], phaseMS["send"], phaseMS["otp"],
			phaseMS["verify"], phaseMS["captcha"], phaseMS["submit"], phaseMS["oauth"])
		result["phase_ms"] = map[string]int{
			"total":   int(total * 1000),
			"init":    int(phaseMS["init"] * 1000),
			"email":   int(phaseMS["email"] * 1000),
			"send":    int(phaseMS["send"] * 1000),
			"otp":     int(phaseMS["otp"] * 1000),
			"verify":  int(phaseMS["verify"] * 1000),
			"captcha": int(phaseMS["captcha"] * 1000),
			"submit":  int(phaseMS["submit"] * 1000),
			"oauth":   int(phaseMS["oauth"] * 1000),
		}
	}

	if r.isCancelled() {
		result["status"] = "cancelled"
		return result
	}

	// Step 1: Initialize config (extract action_id from sign-up page)
	Logf("[Grok] Step 1: 初始化配置...")
	p1 := grokStartPhase("init")
	cfg, err := grokInitConfig(r.client, r.profile)
	logPhase("init", p1.end(), "")
	if err != nil {
		result["error"] = fmt.Sprintf("初始化失败: %v", err)
		finishTiming()
		return result
	}

	if r.isCancelled() {
		result["status"] = "cancelled"
		return result
	}
	if r.stepDelay > 0 {
		sleepWithCancel(time.Duration(r.stepDelay)*time.Second, r.cancel)
	}

	// Step 2: Create email
	var email, password string
	var emailClient *HTTPClient
	var mailtmToken string

	emailMode := r.emailMode
	if emailMode == "" {
		emailMode = "mailtm"
	}

	p2 := grokStartPhase("email")
	switch emailMode {
	case "outlook":
		if r.emailSvc == nil {
			result["error"] = "Outlook 模式但未提供邮箱数据"
			finishTiming()
			return result
		}
		Logf("[Grok] Step 2: 使用 Outlook 邮箱...")
		email = r.emailSvc.Address()
		password = grokRandomPassword()
		result["email"] = email
		Logf("[Grok] 邮箱: %s", email)

	case "cfworker", "edu":
		if r.emailSvc == nil {
			result["error"] = "CF Worker 模式但未配置 Worker"
			finishTiming()
			return result
		}
		Logf("[Grok] Step 2: 使用 CF Worker 创建邮箱...")
		addr, err := r.emailSvc.Create()
		if err != nil {
			result["error"] = fmt.Sprintf("CF Worker 创建邮箱失败: %v", err)
			finishTiming()
			return result
		}
		email = addr
		defer releaseOTPPending(email)
		password = grokRandomPassword()
		result["email"] = email
		Logf("[Grok] 邮箱: %s", email)

	case "managed_temp", "icloud_api", "icloud":
		if r.emailSvc == nil {
			result["error"] = "托管临时邮箱未配置"
			finishTiming()
			return result
		}
		Logf("[Grok] Step 2: 使用托管临时邮箱创建地址...")
		addr, err := r.emailSvc.Create()
		if err != nil {
			// iCloud 模式：文案明确是 iCloud 邮箱（避免「创建临时邮箱失败」
			// 误导用户以为跑了 Mail.tm 临时邮箱）
			if emailMode == "icloud_api" || emailMode == "icloud" {
				result["error"] = fmt.Sprintf("创建 iCloud 邮箱失败: %v", err)
			} else {
				result["error"] = fmt.Sprintf("创建临时邮箱失败: %v", err)
			}
			finishTiming()
			return result
		}
		email = addr
		password = grokRandomPassword()
		result["email"] = email
		Logf("[Grok] 邮箱: %s", email)

	default: // mailtm
		Logf("[Grok] Step 2: 创建 Mail.tm 邮箱...")
		// Mail.tm 与 xAI 会话无关：直连省住宅代理流量（OTP 轮询可达数十次/账号）。
		emailClient = NewHTTPClientWithBrowser("", false, "")
		defer emailClient.Close()
		account, err := mailtmCreateAccount(emailClient)
		if err != nil {
			result["error"] = fmt.Sprintf("创建邮箱失败: %v", err)
			finishTiming()
			return result
		}
		email = account.Email
		mailtmToken = account.Token
		password = grokRandomPassword()
		result["email"] = email
		Logf("[Grok] 邮箱: %s", email)
	}
	logPhase("email", p2.end(), email)

	if r.isCancelled() {
		result["status"] = "cancelled"
		return result
	}
	if r.stepDelay > 0 {
		sleepWithCancel(time.Duration(r.stepDelay)*time.Second, r.cancel)
	}

	// Start free Turnstile ASAP (after email) so it overlaps send+otp+verify.
	provider := captcha.Provider
	if provider == "" {
		provider = CaptchaProviderEzSolver
	}
	captcha.SiteKey = cfg.SiteKey
	if captcha.SiteURL == "" {
		captcha.SiteURL = grokSignUpURL
	}
	captcha.ProxyURL = captchaProxyWithLegacyFallback(captcha, r.proxy)
	if captcha.UserAgent == "" && r.profile != nil {
		captcha.UserAgent = r.profile.UA
	}

	type captchaJob struct {
		res *TurnstileSolveResult
		err error
		sec float64
	}
	var captchaCh chan captchaJob
	if provider != CaptchaProviderNone {
		captchaCh = make(chan captchaJob, 1)
		prefetched := false
		// 流水线预取:命中则直接用预取结果(通常已完成或即将完成),
		// 打码 8s 与上一轮的等码/提交窗口重叠,单任务 15s → ~10s。
		if captcha.Prefetch != nil {
			if pch := captcha.Prefetch.Take(captcha.WorkerID, captcha.ProxyURL); pch != nil {
				prefetched = true
				Logf("[Grok] Step 6∩2-5: 命中流水线预取打码 (worker=%d, solver 已在跑)", captcha.WorkerID)
				go func() {
					pr := <-pch
					captchaCh <- captchaJob{res: pr.res, err: pr.err, sec: pr.sec}
				}()
			}
		}
		if !prefetched {
			Logf("[Grok] Step 6∩2-5: 并行启动 Turnstile (provider=%s) 覆盖发码/等码/验码...", provider)
			go func() {
				t0 := time.Now()
				solveOpts := captcha
				solveOpts.Provider = provider
				res, cerr := SolveTurnstileWithOptions(solveOpts, r.cancel)
				captchaCh <- captchaJob{res: res, err: cerr, sec: time.Since(t0).Seconds()}
			}()
		}
	} else {
		Logf("[Grok] Step 6: 打码关闭 (none)，直接提交空 turnstileToken")
	}

	// Step 3: Send verification code via gRPC-Web (overlaps captcha)
	Logf("[Grok] Step 3: 发送验证码...")
	p3 := grokStartPhase("send")
	if err := r.sendEmailCode(email); err != nil {
		logPhase("send", p3.end(), "fail")
		result["error"] = fmt.Sprintf("发送验证码失败: %v", err)
		finishTiming()
		return result
	}
	logPhase("send", p3.end(), "ok")

	if r.isCancelled() {
		result["status"] = "cancelled"
		return result
	}
	if r.stepDelay > 0 {
		sleepWithCancel(time.Duration(r.stepDelay)*time.Second, r.cancel)
	}

	// Step 4: Poll for verification code (fast interval 500ms)
	Logf("[Grok] Step 4: 等待验证码...")
	if outlookSvc, ok := r.emailSvc.(*OutlookEmail); ok {
		outlookSvc.SetCodeExtractor(grokExtractCode)
	}
	if cfwSvc, ok := r.emailSvc.(*CFWorkerEmail); ok {
		cfwSvc.SetCodeExtractor(grokExtractCode)
	}
	if teSvc, ok := r.emailSvc.(*TempEmail); ok {
		teSvc.SetCodeExtractor(grokExtractCode)
	}
	if mtSvc, ok := r.emailSvc.(*MailTMEmail); ok {
		mtSvc.SetCodeExtractor(grokExtractCode)
	}
	p4 := grokStartPhase("otp")
	var verifyCode string
	switch emailMode {
	case "outlook", "cfworker", "edu", "managed_temp":
		rawCode, err := r.emailSvc.WaitForCode(60*time.Second, r.otpPoll)
		if err != nil {
			logPhase("otp", p4.end(), "fail")
			result["error"] = fmt.Sprintf("获取验证码失败: %v", err)
			finishTiming()
			return result
		}
		verifyCode = strings.ReplaceAll(rawCode, "-", "")
		if len(verifyCode) == 0 {
			logPhase("otp", p4.end(), "empty")
			result["error"] = "获取到的验证码为空"
			finishTiming()
			return result
		}
		Logf("[Grok] 抓到验证码: %s", verifyCode)
	case "icloud_api", "icloud":
		// iCloud 邮件经 Apple HME 转发 + IMAP 同步，实测投递延迟可达
		// 8-10 分钟（SMTP 秒到但 x.ai 发件慢）——给足 15 分钟等待。
		rawCode, err := r.emailSvc.WaitForCode(900*time.Second, r.otpPoll)
		if err != nil {
			logPhase("otp", p4.end(), "fail")
			result["error"] = fmt.Sprintf("获取验证码失败: %v", err)
			finishTiming()
			return result
		}
		verifyCode = strings.ReplaceAll(rawCode, "-", "")
		if len(verifyCode) == 0 {
			logPhase("otp", p4.end(), "empty")
			result["error"] = "获取到的验证码为空"
			finishTiming()
			return result
		}
		Logf("[Grok] 抓到验证码: %s", verifyCode)
	default: // mailtm
		code, err := grokGetVerifyCode(emailClient, mailtmToken, email, r.otpPoll, r.cancel)
		if err != nil {
			logPhase("otp", p4.end(), "fail")
			result["error"] = fmt.Sprintf("获取验证码失败: %v", err)
			finishTiming()
			return result
		}
		verifyCode = code
	}
	logPhase("otp", p4.end(), "ok")

	if r.isCancelled() {
		result["status"] = "cancelled"
		return result
	}
	if r.stepDelay > 0 {
		sleepWithCancel(time.Duration(r.stepDelay)*time.Second, r.cancel)
	}

	// Step 5: Verify email code via gRPC-Web
	Logf("[Grok] Step 5: 验证验证码...")
	p5 := grokStartPhase("verify")
	if err := r.verifyEmailCode(email, verifyCode); err != nil {
		logPhase("verify", p5.end(), "fail")
		result["error"] = fmt.Sprintf("验证码无效: %v", err)
		finishTiming()
		return result
	}
	logPhase("verify", p5.end(), "ok")
	// 验证码通过验证 → 通知调用方及时清理邮箱（iCloud 用完即删）
	if r.onOTPVerified != nil {
		go r.onOTPVerified(email)
	}

	if r.isCancelled() {
		result["status"] = "cancelled"
		return result
	}
	if r.stepDelay > 0 {
		sleepWithCancel(time.Duration(r.stepDelay)*time.Second, r.cancel)
	}

	// Collect parallel captcha (may already be ready after OTP wait).
	var (
		sso            string
		lastCaptchaErr error
		lastSubmitErr  error
		preToken       string
		preRes         *TurnstileSolveResult
		captchaWaited  bool
		solveAttempts  int
	)
	if captchaCh != nil {
		job := <-captchaCh
		solveAttempts = 1
		captchaWaited = true
		// If OTP was slow, captcha wall time is already overlapped; still record solver duration.
		if phaseMS["captcha"] == 0 {
			logPhase("captcha", job.sec, "parallel")
		}
		if job.err != nil || job.res == nil || job.res.Token == "" {
			lastCaptchaErr = job.err
			if lastCaptchaErr == nil {
				lastCaptchaErr = fmt.Errorf("empty turnstile token")
			}
			if captchaSolveAttempts(provider, lastCaptchaErr) > 1 {
				Logf("[Grok] 并行 CAPTCHA 可恢复失败: %v — 提交前用新浏览器重试一次", lastCaptchaErr)
			} else {
				Logf("[Grok] 并行 CAPTCHA 不可恢复失败: %v — 不重复请求", lastCaptchaErr)
			}
		} else {
			preToken = job.res.Token
			preRes = job.res
			Logf("[Grok] 并行 CAPTCHA OK via %s len=%d (solver=%.1fs)", job.res.Provider, len(preToken), job.sec)
		}
		// 打码结果落定 → 通知外层触发下一轮预取(流水线模式)。
		// 无论成败都触发:失败时下一轮 Take 会拿到 err,走既有重试。
		if captcha.OnCaptchaDone != nil {
			captcha.OnCaptchaDone(captcha.WorkerID, captcha.ProxyURL)
		}
	}

	submitT0 := time.Now()
	// Initial parallel solve and subsequent fresh-token retries share one budget.
	maxAttempts := captchaSolveAttemptLimitFor(provider)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if r.isCancelled() {
			result["status"] = "cancelled"
			return result
		}

		captchaToken := ""
		if provider != CaptchaProviderNone {
			// First attempt: reuse parallel token if any; later attempts re-solve.
			if attempt == 0 && preToken != "" {
				captchaToken = preToken
				if preRes != nil {
					applyTurnstileBrowserContext(r, preRes, true)
				}
			} else {
				if solveAttempts >= maxAttempts {
					break
				}
				solveAttempts++
				tSolve := time.Now()
				solveOpts := captcha
				solveOpts.Provider = provider
				res, cerr := SolveTurnstileWithOptions(solveOpts, r.cancel)
				sec := time.Since(tSolve).Seconds()
				if !captchaWaited {
					logPhase("captcha", sec, fmt.Sprintf("attempt=%d", attempt+1))
					captchaWaited = true
				} else {
					Logf("[Grok] [耗时] captcha_retry: %.1fs (attempt %d)", sec, attempt+1)
					phaseMS["captcha"] += sec
				}
				if cerr != nil || res == nil || res.Token == "" {
					lastCaptchaErr = cerr
					if lastCaptchaErr == nil {
						lastCaptchaErr = fmt.Errorf("empty turnstile token")
					}
					Logf("[Grok] CAPTCHA 失败 (尝试 %d/%d, provider=%s): %v", attempt+1, maxAttempts, provider, lastCaptchaErr)
					errText := strings.ToLower(lastCaptchaErr.Error())
					if strings.Contains(errText, "yescaptcha key") ||
						strings.Contains(errText, "yes captcha") ||
						strings.Contains(errText, "nextcaptcha") ||
						strings.Contains(errText, "next captcha") ||
						strings.Contains(errText, "ezsolver 不可达") ||
						strings.Contains(errText, "未配置") {
						break
					}
					// 打码失败(非配置错误)自动重试冲 100%:一次性打码快(~7s),重试成本低;
					// OTP 已验证通过,continue 只重「打码+提交」,不重复发码/收码。丝滑:不额外 sleep,立即重打。
					continue
				}
				captchaToken = res.Token
				lastCaptchaErr = nil
				applyTurnstileBrowserContext(r, res, true)
				Logf("[Grok] CAPTCHA OK via %s len=%d", res.Provider, len(captchaToken))
			}
		}

		// Step 7: Submit registration (bounded concurrent POSTs)
		select {
		case grokPostSem <- struct{}{}:
		case <-r.cancel.Done():
			result["status"] = "cancelled"
			return result
		}
		Logf("[Grok] Step 7: 提交注册 (尝试 %d/%d)...", attempt+1, maxAttempts)
		payload := []map[string]interface{}{
			{
				"emailValidationCode": verifyCode,
				"createUserAndSessionRequest": map[string]interface{}{
					"email":              email,
					"givenName":          grokRandomName(),
					"familyName":         grokRandomName(),
					"clearTextPassword":  password,
					"tosAcceptedVersion": "$undefined",
				},
				"turnstileToken":         captchaToken,
				"promptOnDuplicateEmail": true,
			},
		}
		payloadBytes, _ := json.Marshal(payload)

		submitHeaders := OrderedHeaders{
			{"User-Agent", r.profile.UA},
			{"Accept", "text/x-component"},
			{"Content-Type", "text/plain;charset=UTF-8"},
			{"Origin", grokSiteURL},
			{"Referer", grokSiteURL + "/sign-up"},
			{"Next-Router-State-Tree", cfg.StateTree},
			{"Next-Action", cfg.ActionID},
		}
		// Weave coherent Client Hints + Fetch Metadata into the Next.js server-action
		// POST so the account-creating request matches the claimed Chrome (real Chrome
		// sends these on the server action). Only for Chromium profiles.
		if r.profile.IsChromiumBased() {
			submitHeaders = append(submitHeaders,
				[]string{"sec-ch-ua", r.profile.SecCHUA},
				[]string{"sec-ch-ua-mobile", "?0"},
				[]string{"sec-ch-ua-platform", `"` + r.profile.Platform + `"`},
				[]string{"Sec-Fetch-Site", SecFetchSiteSame},
				[]string{"Sec-Fetch-Mode", SecFetchModeCors},
				[]string{"Sec-Fetch-Dest", SecFetchDestEmpty},
				[]string{"Accept-Encoding", AcceptEncoding},
				[]string{"Accept-Language", AcceptLangEN},
			)
		}
		cs := grokSubmitCookieHeader(r.client.Cookies())
		if cs != "" {
			submitHeaders = append(submitHeaders, []string{"Cookie", cs})
		}

		resp, err := r.client.PostRawOrdered(grokSignUpURL, submitHeaders, payloadBytes)
		<-grokPostSem
		if err != nil {
			Logf("[Grok] 提交请求失败: %v", err)
			lastSubmitErr = fmt.Errorf("提交请求失败: %w", err)
			break
		}

		if resp.StatusCode != 200 {
			preview := truncateStr(string(resp.Body), 240)
			lastSubmitErr = fmt.Errorf("提交失败 HTTP %d: %s", resp.StatusCode, preview)
			Logf("[Grok] %v", lastSubmitErr)
			if grokSubmitNeedsFreshCaptcha(resp.StatusCode, string(resp.Body)) && attempt+1 < maxAttempts {
				sleepWithCancel(200*time.Millisecond, r.cancel)
				continue
			}
			break
		}

		// Step 8: Extract SSO token from set-cookie redirect
		body := string(resp.Body)
		m := grokSetCookieRegex.FindStringSubmatch(body)
		if len(m) < 2 {
			preview := body
			if len(preview) > 500 {
				preview = preview[:500]
			}
			safePreview := sanitizeGrokLogText(preview)
			lastSubmitErr = fmt.Errorf("注册响应未返回 SSO 跳转: %s", safePreview)
			Logf("[Grok] 未找到 set-cookie URL, 响应体=%d字节 摘要=%s", len(body), safePreview)
			if grokSubmitNeedsFreshCaptcha(resp.StatusCode, body) && attempt+1 < maxAttempts {
				continue
			}
			break
		}

		verifyURL := m[1]
		Logf("[Grok] Step 8: 跟随 set-cookie 重定向...")
		// Follow the full redirect chain to get SSO cookie (client has DisableRedirects: true).
		// Each hop is a top-level document navigation, so send coherent navigation headers
		// (Client Hints + Fetch Metadata) instead of only UA+Accept. Sec-Fetch-Site is
		// computed per hop from the previous URL; the submit was posted from grokSignUpURL,
		// so that seeds the first hop's site.
		redirectResp, err := r.client.GetOrdered(verifyURL, RedirectNavHeaders(r.profile, verifyURL, grokSignUpURL, nil))
		if err != nil {
			Logf("[Grok] 重定向请求失败: %v", err)
			continue
		}
		r.client.SaveCookies(redirectResp)

		// Manually follow 3xx redirects (up to 10 hops)
		currentURL := verifyURL
		for i := 0; i < 10; i++ {
			if redirectResp.StatusCode < 300 || redirectResp.StatusCode >= 400 {
				break
			}
			loc := redirectResp.Location()
			if loc == "" {
				break
			}
			// Resolve relative Location (e.g. /account) against current URL.
			if strings.HasPrefix(loc, "/") || !strings.Contains(loc, "://") {
				if base, perr := url.Parse(currentURL); perr == nil {
					if ref, rerr := url.Parse(loc); rerr == nil {
						loc = base.ResolveReference(ref).String()
					}
				}
			}
			Logf("[Grok] 跟随重定向 (%d) -> %s", redirectResp.StatusCode, safeGrokURLForLog(loc))
			redirectResp, err = r.client.GetOrdered(loc, RedirectNavHeaders(r.profile, loc, currentURL, nil))
			if err != nil {
				Logf("[Grok] 重定向请求失败: %v", err)
				break
			}
			r.client.SaveCookies(redirectResp)
			currentURL = loc
		}

		sso = r.client.GetCookie("sso")
		if sso != "" {
			break
		}
		Logf("[Grok] 未获取到 SSO cookie")
	}

	logPhase("submit", time.Since(submitT0).Seconds(), "")

	if sso == "" {
		if lastCaptchaErr != nil {
			result["error"] = fmt.Sprintf("Turnstile 打码失败: %v", lastCaptchaErr)
		} else if lastSubmitErr != nil {
			result["error"] = lastSubmitErr.Error()
		} else {
			result["error"] = "注册提交失败或未获取到 SSO token"
		}
		// Invalidate ActionID cache on hard submit failures that look like action mismatch.
		if lastCaptchaErr == nil {
			grokInvalidateConfigCache()
		}
		finishTiming()
		return result
	}

	// Persist cookie aliases for export / chat / sub2api SSO import.
	ssoRw := strings.TrimSpace(r.client.GetCookie("sso-rw"))
	if ssoRw == "" {
		ssoRw = sso
	}
	sso = normalizeGrokSSOToken(sso)
	ssoRw = normalizeGrokSSOToken(ssoRw)
	if ssoRw == "" {
		ssoRw = sso
	}

	result["password"] = password
	result["sso"] = sso
	result["sso_token"] = sso
	result["sso_rw"] = ssoRw
	result["sso-rw"] = ssoRw
	result["key"] = sso
	result["created_at"] = time.Now().Format("2006-01-02 15:04:05")

	// Step 9: 验活 — accounts.x.ai session + sub2api device OAuth convert.
	if r.skipVerify {
		result["verify_status"] = "skipped"
		result["status"] = "success"
		// Explicit 注册成功 so admin log tab "注册成功" matches default skip_verify path.
		Logf("[Grok] ✓ 注册成功(跳过验活): %s", email)
		Logf("[Grok] Step 9: 已跳过验活 (skip_verify=true) | %s", email)
		Logf("[Grok] SSO cookie 已获取 (len=%d)", len(sso))
		finishTiming()
		return result
	}

	Logf("[Grok] Step 9: 验活 SSO (accounts.x.ai + device OAuth)...")
	p9 := grokStartPhase("oauth")
	verifyCtx, cancelVerify := grokRegistrationVerifyContext(r.cancel)
	verifyStatus, oauthTok, verr := grokVerifyRegisteredSSO(verifyCtx, sso, ssoRw, r.proxy, r.proxy != "")
	cancelVerify()
	logPhase("oauth", p9.end(), verifyStatus)
	result["verify_status"] = verifyStatus
	if verifyStatus == "cancelled" {
		result["status"] = "cancelled"
		result["error"] = "注册已取消"
		Logf("[Grok] 注册验活已取消: %s", email)
		finishTiming()
		return result
	}
	if verifyStatus == "dead" {
		result["status"] = "failed"
		result["error"] = fmt.Sprintf("SSO 验活失败（账号无效）: %v", verr)
		Logf("[Grok] ✗ 注册假成功: %s | %v", email, verr)
		finishTiming()
		return result
	}
	if oauthTok != nil {
		result["access_token"] = oauthTok.AccessToken
		result["refresh_token"] = oauthTok.RefreshToken
		result["id_token"] = oauthTok.IDToken
		result["token_type"] = oauthTok.TokenType
		result["expires_in"] = oauthTok.ExpiresIn
		result["oauth_scope"] = oauthTok.Scope
		if oauthTok.Email != "" {
			result["oauth_email"] = oauthTok.Email
		}
		if oauthTok.Subject != "" {
			result["oauth_sub"] = oauthTok.Subject
		}
		result["base_url"] = grokCLIChatBaseURL
		Logf("[Grok] ✓ 注册+OAuth验活成功: %s | verify=%s | access=%s...", email, verifyStatus, oauthTok.AccessToken[:min(12, len(oauthTok.AccessToken))])
	} else {
		if verr != nil {
			result["verify_error"] = verr.Error()
			Logf("[Grok] ✓ 注册成功(SSO存活, OAuth转换失败可稍后重试): %s | %v", email, verr)
		} else {
			Logf("[Grok] ✓ 注册成功(SSO存活): %s", email)
		}
	}
	result["status"] = "success"
	Logf("[Grok] SSO cookie 已获取 (len=%d)", len(sso))
	finishTiming()
	return result
}

func grokSubmitNeedsFreshCaptcha(status int, body string) bool {
	if status != 200 && status != 400 && status != 401 && status != 403 && status != 422 {
		return false
	}
	lower := strings.ToLower(body)
	for _, marker := range []string{"turnstile", "captcha", "challenge", "cf-turnstile-response"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
