package grokregister

import (
	"crypto/rand"
	"encoding/hex"
	mrand "math/rand"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// safeGrokURLForLog keeps enough redirect context for diagnostics without
// persisting query/fragment credentials (the xAI SSO chain carries long tokens).
func safeGrokURLForLog(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" {
		return "<redacted-url>"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	path := u.EscapedPath()
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if len(part) > 48 {
			parts[i] = "<redacted>"
		}
	}
	return u.Scheme + "://" + u.Host + strings.Join(parts, "/")
}

// sanitizeGrokLogText removes URL query/fragment data from short upstream body
// previews. It intentionally errs on the side of hiding malformed URL-like text.
func sanitizeGrokLogText(text string) string {
	return grokURLLikeRegex.ReplaceAllStringFunc(text, safeGrokURLForLog)
}

var grokURLLikeRegex = regexp.MustCompile(`https?://[^\s"'<>]+`)

func isCancelled(cancel *SafeCancel) bool {
	return cancel != nil && cancel.IsClosed()
}

func sleepWithCancel(d time.Duration, cancel *SafeCancel) bool {
	if cancel == nil {
		time.Sleep(d)
		return false
	}
	select {
	case <-cancel.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(mrand.Intn(256))
		}
	}
	return hex.EncodeToString(b)
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(mrand.Intn(256))
		}
	}
	// URL-safe base64 without padding
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

const (
	otpCompletedTTL        = 10 * time.Minute
	maxOTPCompletedEntries = 4096
)

// otpCache: email -> OTPEntry (defined in email_providers.go)
var otpCache sync.Map
var otpPending sync.Map

var (
	otpLifecycleMu sync.Mutex
	otpCompleted   = make(map[string]time.Time)
)

// otpEmailAliasKeys returns the canonical address plus its root/edu dual form.
// OTP is always keyed by full RCPT; root and edu.<root> must resolve to the same
// mailbox so enabling edu format never makes bare-domain lookups miss.
//
//	user@example.com      <-> user@edu.example.com
//	user@edu.example.com  <-> user@example.com
func otpEmailAliasKeys(email string) []string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return []string{email}
	}
	local := email[:at]
	domain := email[at+1:]
	keys := []string{email}
	var alt string
	if strings.HasPrefix(domain, "edu.") {
		bare := strings.TrimPrefix(domain, "edu.")
		if bare != "" {
			alt = local + "@" + bare
		}
	} else {
		alt = local + "@edu." + domain
	}
	if alt != "" && alt != email {
		keys = append(keys, alt)
	}
	return keys
}

func pruneOTPCompleted(completed map[string]time.Time, now time.Time, max int) {
	for email, expiresAt := range completed {
		if !expiresAt.After(now) {
			delete(completed, email)
		}
	}
	if max < 0 {
		max = 0
	}
	for len(completed) > max {
		oldestEmail := ""
		var oldestExpiry time.Time
		for email, expiresAt := range completed {
			if oldestEmail == "" || expiresAt.Before(oldestExpiry) {
				oldestEmail = email
				oldestExpiry = expiresAt
			}
		}
		if oldestEmail == "" {
			break
		}
		delete(completed, oldestEmail)
	}
}

func registerOTPPending(email string) {
	keys := otpEmailAliasKeys(email)
	if len(keys) == 0 {
		return
	}
	otpLifecycleMu.Lock()
	defer otpLifecycleMu.Unlock()
	pruneOTPCompleted(otpCompleted, time.Now(), maxOTPCompletedEntries)
	for _, k := range keys {
		delete(otpCompleted, k)
		otpPending.Store(k, struct{}{})
	}
}

func releaseOTPPending(email string) {
	keys := otpEmailAliasKeys(email)
	if len(keys) == 0 {
		return
	}
	now := time.Now()
	otpLifecycleMu.Lock()
	defer otpLifecycleMu.Unlock()
	for _, k := range keys {
		otpPending.Delete(k)
		otpCache.Delete(k)
		otpCompleted[k] = now.Add(otpCompletedTTL)
	}
	pruneOTPCompleted(otpCompleted, now, maxOTPCompletedEntries)
}

func trimOTPCache(max int) {
	if max <= 0 {
		return
	}
	n := 0
	otpCache.Range(func(k, _ any) bool {
		n++
		return true
	})
	if n <= max {
		return
	}
	// Drop arbitrary excess (FIFO not required; next WaitForCode re-polls Worker/KV).
	drop := n - max
	otpCache.Range(func(k, _ any) bool {
		if drop <= 0 {
			return false
		}
		// Never drop addresses currently waiting.
		if _, pending := otpPending.Load(k); pending {
			return true
		}
		otpCache.Delete(k)
		drop--
		return true
	})
}

// loadOTPCacheEntry returns a push-cache hit for email or its root/edu dual form.
func loadOTPCacheEntry(email string) (OTPEntry, bool) {
	for _, k := range otpEmailAliasKeys(email) {
		if val, ok := otpCache.Load(k); ok {
			return val.(OTPEntry), true
		}
	}
	return OTPEntry{}, false
}

// deleteOTPCacheEntry removes all dual-form keys for a mailbox.
func deleteOTPCacheEntry(email string) {
	for _, k := range otpEmailAliasKeys(email) {
		otpCache.Delete(k)
	}
}

// StorePushedOTP caches a Worker-pushed OTP after EduEmailService has validated
// the per-worker API key + domain. Never-seen and pre-WaitForCode callbacks are
// accepted because delivery can race ahead of the waiter. Once a mailbox
// lifecycle is released, its completed tombstone rejects late callbacks until
// expiry or a new registerOTPPending call starts another lifecycle.
//
// Root and edu.<root> forms are dual-written so bare-domain and edu-format
// waiters share the same OTP regardless of which RCPT form the Worker reported.
//
// Security: only authenticated callbacks reach this function. Cache is bounded.
func StorePushedOTP(email, code, body string) bool {
	keys := otpEmailAliasKeys(email)
	if len(keys) == 0 {
		return false
	}
	code = strings.TrimSpace(code)
	if code == "" && strings.TrimSpace(body) == "" {
		return false
	}
	now := time.Now()
	otpLifecycleMu.Lock()
	defer otpLifecycleMu.Unlock()
	pruneOTPCompleted(otpCompleted, now, maxOTPCompletedEntries)
	for _, k := range keys {
		if expiresAt, completed := otpCompleted[k]; completed && expiresAt.After(now) {
			return false
		}
	}
	entry := OTPEntry{Code: code, Body: body}
	for _, k := range keys {
		otpCache.Store(k, entry)
	}
	// Bound memory if workers spam (authenticated) keys without active jobs.
	trimOTPCache(2048)
	return true
}

// shouldRetryJobWithNewProxy reports errors that often recover after switching residential exit.
func shouldRetryJobWithNewProxy(errMsg string) bool {
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "" {
		return false
	}
	// Pure captcha/EzSolver host failures: do NOT switch proxy (was mislabeled 代理失败).
	if isProxyIrrelevantFailure(errMsg) {
		return false
	}
	keys := []string{
		"cloudflare",
		"http 403",
		// 中文错误串里的 403（如「发送验证码返回 403: ...not available in your
		// region」）此前只有 "http 403" 一条窄匹配，完全命中不到 → 地区封锁
		// 的出口不换代理就直接把账号判失败。403/451 全形态都要换出口重试。
		"返回 403",
		"状态 403",
		"403 forbidden",
		"not available in your region",
		"unavailable in your region",
		"blocked in your country",
		"http 451",
		"返回 451",
		"更换住宅代理",
		"更换代理",
		"访问注册页面失败",
		"初始化失败",
		"i/o timeout",
		"deadline exceeded",
		"connection reset",
		"socks",
		"proxy",
		"sso",
		"注册提交失败",
		"未获取到 sso",
		"提交注册",
		// 代理出口连不上目标（含打码走注册代理时的 solve 连接类失败）
		"could not be reached",
		"failed to connect",
		"connection refused",
		"target host",
	}
	for _, k := range keys {
		if strings.Contains(e, k) || strings.Contains(errMsg, k) {
			return true
		}
	}
	// Chinese CF message
	if strings.Contains(errMsg, "Cloudflare") || strings.Contains(errMsg, "注册页") || strings.Contains(errMsg, "SSO") || strings.Contains(errMsg, "提交失败") {
		return true
	}
	return false
}

// isProxyIrrelevantFailure: host-side captcha/solver / user-cancel must not eliminate residential proxies.
func isProxyIrrelevantFailure(errMsg string) bool {
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "" {
		return false
	}
	// 打码走注册代理时（SolveViaProxy），solve 的 HTTP 经代理出口发往
	// accounts.x.ai——"target host could not be reached" / 连接类错误就是
	// 代理出口连不上目标，属于代理问题：允许换代理重试，而不是继续用
	// 烂代理空耗。仅在 SolveViaProxy 模式下放宽（服务端打码模式仍不换）。
	if solveViaProxyEnabled() {
		for _, k := range []string{"could not be reached", "failed to connect", "connection refused", "connect:", "target host", "proxy connect"} {
			if strings.Contains(e, k) {
				return false
			}
		}
	}
	// Task stop / cancel is never a proxy death signal.
	if e == "cancelled" || e == "canceled" || strings.Contains(e, "context canceled") || strings.Contains(e, "context cancelled") {
		return true
	}
	keys := []string{
		"ezsolver", "turnstile", "captcha", "打码", "yescaptcha", "nextcaptcha",
		// 引擎名要整体列入：此前只有 "auralith http 408" / "veloraturn go http 503"
		// 这两条窄匹配，于是 "auralith rate limit"、"veloraturn 连接失败" 之类的
		// 打码故障会被归成代理失败——代理白挨淘汰，真正的打码问题反被掩盖。
		"auralith", "veloraturn",
		"token not obtained", "client.timeout",
		"/solve", "warmup", "预热",
		// captcha queue timeout often surfaces as generic deadline; still not residential proxy
		"solve timeout", "打码超时", "no captcha",
		// stop/teardown: free solvers reject new solves while Chrome is draining
		"browser release in progress",
	}
	for _, k := range keys {
		if strings.Contains(e, k) || strings.Contains(errMsg, k) {
			return true
		}
	}
	return false
}

// isRegionBlockedFailure 判断失败是否为「出口所在地区被 x.ai 封锁」。
// 形态: 发送验证码返回 403 + "This service is not available in your region"。
// 这不是账号问题也不是打码问题,而是该出口 IP 的地理位置不被接受 ——
// 必须换出口重试,并把该代理计入失败以便冷却,否则同一个被封出口
// 会被反复选中,每次都在「发送验证码」这一步白烧一个邮箱。
func isRegionBlockedFailure(errMsg string) bool {
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "" {
		return false
	}
	for _, k := range []string{
		"not available in your region",
		"unavailable in your region",
		"blocked in your country",
		"not available in your country",
	} {
		if strings.Contains(e, k) {
			return true
		}
	}
	return false
}

// isProxyDNSFailure 判断失败是否为「代理出口 DNS 解析失败」。
// 典型形态: auralith HTTP 400 {"error":"target host could not be safely resolved"}。
// 打码/注册请求经代理出口解析业务域名(x.ai 系)失败 = 代理 DNS 坏的确定性信号,
// 好代理绝不会解析不了业务域名 —— 这类失败不能按「打码无关」豁免,
// 必须计入代理失败并走淘汰链路,否则坏 DNS 代理会被无限复用。
func isProxyDNSFailure(errMsg string) bool {
	e := strings.ToLower(strings.TrimSpace(errMsg))
	for _, k := range []string{
		"could not be safely resolved",
		"no such host",
		"server misbehaving",
		"temporary failure in name resolution",
		"nodename nor servname provided",
		"lookup ",
	} {
		if strings.Contains(e, k) {
			return true
		}
	}
	return false
}

// isRateLimitFailure 判断失败是否为「上游限流」而不是「代理坏了」。
//
// 这两者的处置完全相反：代理坏了要淘汰；被限流的代理自身是好的，
// 只是当前出口 IP 被上游临时掐了，冷却一段时间后还能用。此前没有显式判定，
// 429 只能靠 shouldRetryJobWithNewProxy 里的通用模式偶然命中，日志里也
// 区分不出「代理死了」和「被限流」。
//
// 注意顺序：打码侧失败优先——打码服务自己返回 429 不是注册出口被限流。
func isRateLimitFailure(errMsg string) bool {
	if isProxyIrrelevantFailure(errMsg) {
		return false
	}
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "" {
		return false
	}
	keys := []string{
		"http 429", "status 429", "statuscode 429", "code 429",
		"rate limit", "rate-limit", "ratelimit", "rate limited",
		"too many requests", "too many attempts",
		"请求过于频繁", "频率限制", "访问过于频繁", "限流",
	}
	for _, k := range keys {
		if strings.Contains(e, k) || strings.Contains(errMsg, k) {
			return true
		}
	}
	// 裸 429 需谨慎：只在明确是状态码位置时才算，避免匹配到耗时/字节数里的 429
	return strings.Contains(e, " 429 ") || strings.HasSuffix(e, " 429")
}

// isUserCancelledStatus reports batch/job cancel (stop button / shutdown).
func isUserCancelledStatus(status, errMsg string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	if s == "cancelled" || s == "canceled" {
		return true
	}
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "cancelled" || e == "canceled" {
		return true
	}
	// Cover solver teardown during Stop: workers surface cancel as captcha/HTTP errors.
	for _, marker := range []string{
		"context canceled", "context cancelled",
		"browser release in progress",
		"operation was canceled", "operation was cancelled",
	} {
		if strings.Contains(e, marker) {
			return true
		}
	}
	return false
}
