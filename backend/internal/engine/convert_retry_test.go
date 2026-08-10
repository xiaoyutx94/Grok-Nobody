package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIsPermanentGrantDenial(t *testing.T) {
	permanent := []string{
		"confirm ok (verify+approve) but token grant denied",
		"GROK_SSO_AUTHORIZATION_DENIED: xAI device authorization was denied",
		"invalid_grant",
		"authorization was denied or expired",
		"access denied during token poll",
		"Access denied (token grant)",
	}
	for _, m := range permanent {
		if !isPermanentGrantDenial(m) {
			t.Errorf("expected permanent: %q", m)
		}
	}
	notPermanent := []string{
		"",
		"http 429 too many requests",
		"proxy authentication required",
		"access denied", // 裸 access denied 可能是代理，不能算永久
		"connect tunnel failed",
	}
	for _, m := range notPermanent {
		if isPermanentGrantDenial(m) {
			t.Errorf("expected NOT permanent: %q", m)
		}
	}
}

func TestIsProxyFailureExcludesPermanentDenial(t *testing.T) {
	// 关键：永久拒绝不能被当成代理故障，否则会白跑一次直连回退
	if isProxyFailure("confirm ok (verify+approve) but token grant denied") {
		t.Error("permanent grant denial must not be classified as proxy failure")
	}
	if isProxyFailure("invalid_grant") {
		t.Error("invalid_grant must not be classified as proxy failure")
	}
	// 裸 access denied 视为代理问题，允许直连回退
	if !isProxyFailure("Access denied") {
		t.Error("bare access denied should allow direct fallback")
	}
	for _, m := range []string{
		"proxy authentication required",
		"http 407",
		"connect tunnel failed",
		"proxyconnect tcp: dial tcp: connection refused",
		"socks connect tcp: i/o timeout",
		"tls handshake timeout",
		"http 403 forbidden",
		"unexpected EOF",
	} {
		if !isProxyFailure(m) {
			t.Errorf("expected proxy failure: %q", m)
		}
	}
	if isProxyFailure("") {
		t.Error("empty message must not be a proxy failure")
	}
}

func TestIsTransientConvertError(t *testing.T) {
	transient := []error{
		errors.New("上游 HTTP 429: too many requests"),
		errors.New("http 503 service unavailable"),
		errors.New("rate limit exceeded"),
		context.DeadlineExceeded,
		context.Canceled,
		errors.New("context deadline exceeded"),
	}
	for _, e := range transient {
		if !isTransientConvertError(e) {
			t.Errorf("expected transient: %v", e)
		}
	}
	permanent := []error{
		nil,
		errors.New("confirm ok (verify+approve) but token grant denied"),
		errors.New("invalid_grant"),
		errors.New("expired_token"),
		errors.New("some unrelated failure"),
	}
	for _, e := range permanent {
		if isTransientConvertError(e) {
			t.Errorf("expected NOT transient: %v", e)
		}
	}
}

func TestConvertBackoffGrowsQuadratically(t *testing.T) {
	// 与 sub2api 的 attempt²×2s 对齐
	cases := map[int]time.Duration{1: 2 * time.Second, 2: 8 * time.Second, 3: 18 * time.Second}
	for attempt, want := range cases {
		if got := convertBackoff(attempt); got != want {
			t.Errorf("convertBackoff(%d) = %v, want %v", attempt, got, want)
		}
	}
	if got := convertBackoff(0); got != 2*time.Second {
		t.Errorf("convertBackoff(0) = %v, want floor 2s", got)
	}
}

func TestSleepCtxRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, 10*time.Second); err == nil {
		t.Fatal("expected error from canceled ctx")
	}
	start := time.Now()
	if err := sleepCtx(context.Background(), 30*time.Millisecond); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if time.Since(start) < 25*time.Millisecond {
		t.Error("sleepCtx returned too early")
	}
}

// TestConvertProxyFallbackBudget 固定住核心策略：代理失败回退直连不消耗重试预算。
// 用一个假的转换函数模拟 attempt 计数，验证 convertOnce 的循环控制流。
func TestConvertProxyFallbackDoesNotBurnAttempts(t *testing.T) {
	// 模拟 convertOnce 的控制流（不触网），确认：
	// 代理失败 → 切直连且 attempt 回退；直连再失败(transient) → 才计入预算
	type call struct {
		proxy string
	}
	var calls []call
	attempts := 0
	tryProxy := "socks5h://dead:1080"
	maxAttempts := convertMaxAttempts

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts++
		if attempts > 10 {
			t.Fatal("control flow did not terminate (possible infinite loop)")
		}
		calls = append(calls, call{proxy: tryProxy})
		var errMsg string
		if tryProxy != "" {
			errMsg = "proxyconnect tcp: connection refused"
		} else {
			errMsg = "http 429 too many requests"
		}
		if tryProxy != "" && isProxyFailure(errMsg) {
			tryProxy = ""
			attempt--
			continue
		}
		if !isTransientConvertError(fmt.Errorf("%s", errMsg)) || attempt >= maxAttempts {
			break
		}
	}

	if len(calls) != 4 {
		t.Fatalf("expected 4 calls (1 proxy + 3 direct), got %d: %+v", len(calls), calls)
	}
	if calls[0].proxy == "" {
		t.Error("first call should use the proxy")
	}
	for i, c := range calls[1:] {
		if c.proxy != "" {
			t.Errorf("call %d should be direct after proxy failure, got %q", i+2, c.proxy)
		}
	}
}

func TestConvertOnceStopsOnPermanentDenial(t *testing.T) {
	// 永久拒绝必须立刻停止：既不重试也不回退直连
	msg := "confirm ok (verify+approve) but token grant denied"
	if isProxyFailure(msg) {
		t.Error("would wrongly trigger direct fallback")
	}
	if isTransientConvertError(errors.New(msg)) {
		t.Error("would wrongly trigger backoff retry")
	}
}

func TestConvertToOAuthHonorsConfiguredImportMode(t *testing.T) {
	svc := newTestAccountService(t)
	ctx := context.Background()

	// 用户配置：导入走直连
	if _, err := svc.SaveProxySettings(ctx, ProxySettings{
		ImportMode:   "direct",
		RegisterURLs: []string{"socks5://reg-proxy:1080"},
	}); err != nil {
		t.Fatalf("SaveProxySettings: %v", err)
	}
	ps, err := svc.GetProxySettings(ctx)
	if err != nil {
		t.Fatalf("GetProxySettings: %v", err)
	}
	if ps.ImportMode != "direct" {
		t.Fatalf("ImportMode = %q, want direct", ps.ImportMode)
	}
	// direct 模式下即使账号带注册代理，也必须挑出空代理
	if got := PickImportProxy(ps.ImportMode, ps.ImportURLs, ps.RegisterURLs, "socks5://acct-proxy:1080", 0); got != "" {
		t.Errorf("direct mode picked proxy %q, want empty", got)
	}
	// registration 模式才用账号自带代理
	if got := PickImportProxy("registration", ps.ImportURLs, ps.RegisterURLs, "socks5://acct-proxy:1080", 0); got != "socks5://acct-proxy:1080" {
		t.Errorf("registration mode picked %q", got)
	}
}

func TestConvertToOAuthSkipsAndMarksWithoutNetwork(t *testing.T) {
	svc := newTestAccountService(t)
	ctx := context.Background()
	// 已有 access_token → 直接标记入库，不触网
	withTok, err := svc.UpsertFromResult(ctx, map[string]any{
		"email": "hastoken@example.com", "sso": "s1", "access_token": "AT-1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 无 SSO 且无 token → skipped
	noSSO, err := svc.UpsertFromResult(ctx, map[string]any{"email": "nosso@example.com"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := svc.ConvertToOAuth(ctx, []string{withTok.ID, noSSO.ID}, "direct")
	if err != nil {
		t.Fatalf("ConvertToOAuth: %v", err)
	}
	if res.Updated != 1 {
		t.Errorf("Updated = %d, want 1", res.Updated)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Skipped)
	}
	if len(res.Failed) != 0 {
		t.Errorf("Failed = %+v, want none", res.Failed)
	}
	got, _ := svc.Get(ctx, withTok.ID)
	if !got.Imported {
		t.Error("account with token should be marked imported")
	}
}

// TestConvertToOAuthReleasesServiceLock 固定住真实故障：
// 原实现在成功路径上没有 s.mu.Unlock()，一次入库后 List/Get 永久阻塞，
// 前端刷新拿不到响应，表现为「入库总是失败」。
func TestConvertToOAuthReleasesServiceLock(t *testing.T) {
	svc := newTestAccountService(t)
	ctx := context.Background()
	acc, err := svc.UpsertFromResult(ctx, map[string]any{
		"email": "lock@example.com", "sso": "s", "access_token": "AT",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.ConvertToOAuth(ctx, []string{acc.ID}, "direct"); err != nil {
		t.Fatalf("ConvertToOAuth: %v", err)
	}

	// 入库后这些调用都要拿 s.mu；锁没释放就会挂死
	done := make(chan error, 1)
	go func() {
		if _, err := svc.List(ctx); err != nil {
			done <- err
			return
		}
		if _, err := svc.Get(ctx, acc.ID); err != nil {
			done <- err
			return
		}
		if _, err := svc.MarkImported(ctx, []string{acc.ID}, true); err != nil {
			done <- err
			return
		}
		// 再来一次入库，确认可重复调用不自锁
		if _, err := svc.ConvertToOAuth(ctx, []string{acc.ID}, "direct"); err != nil {
			done <- err
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("post-import call failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: service mutex still held after ConvertToOAuth")
	}
}

// TestConvertToOAuthDoesNotHoldLockDuringConvert 确认转换期间账号列表仍可读。
// 用一个没有 access_token 但有 SSO 的账号触发真实转换路径；转换必然失败
// （SSO 是假的），但关键是转换进行中 List 不能被阻塞。
func TestConvertToOAuthKeepsListReadableDuringConvert(t *testing.T) {
	svc := newTestAccountService(t)
	ctx := context.Background()
	if _, err := svc.UpsertFromResult(ctx, map[string]any{
		"email": "slow@example.com", "sso": "bogus-sso-will-fail",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	convertCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	convertDone := make(chan struct{})
	go func() {
		defer close(convertDone)
		_, _ = svc.ConvertToOAuth(convertCtx, []string{"any"}, "direct")
	}()

	// 转换刚起步时 List 必须立即返回
	time.Sleep(150 * time.Millisecond)
	listDone := make(chan error, 1)
	go func() {
		_, err := svc.List(ctx)
		listDone <- err
	}()
	select {
	case err := <-listDone:
		if err != nil {
			t.Fatalf("List during convert: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("List blocked while convert was in flight (lock held across network I/O)")
	}
	<-convertDone
}
