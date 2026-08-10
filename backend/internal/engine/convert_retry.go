package engine

// 入库转换的错误分类与重试策略，与 sub2api 的 convertSSOUnderGate 对齐：
//   - 代理侧失败 → 直连回退一次，且不消耗重试次数
//   - 上游限流/网关抖动 → 指数退避重试（最多 3 次）
//   - 设备流永久拒绝 → 立即停止，不浪费次数
// 对齐来源：backend/internal/service/grok_register_service.go
//   isGrokConvertProxyFailure / grokSSOImportIsTransient / isGrokSSOPermanentGrantDenialMsg

import (
	"context"
	"errors"
	"strings"
	"time"
)

// convertMaxAttempts 与 sub2api 的 maxAttempts 一致。
const convertMaxAttempts = 3

// isPermanentGrantDenial 报告设备流 token 侧的永久拒绝（重试和换代理都救不回来）。
func isPermanentGrantDenial(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	if strings.Contains(m, "confirm ok (verify+approve) but token grant denied") {
		return true
	}
	if strings.Contains(m, "grok_sso_authorization_denied") ||
		strings.Contains(m, "authorization was denied") ||
		strings.Contains(m, "authorization_denied") {
		return true
	}
	if strings.Contains(m, "invalid_grant") {
		return true
	}
	// 「access denied」也可能是代理返回的，只有与 token 轮询同时出现才算永久拒绝
	if strings.Contains(m, "access denied") &&
		(strings.Contains(m, "token poll") || strings.Contains(m, "token polling") || strings.Contains(m, "token grant")) {
		return true
	}
	return false
}

// isProxyFailure 报告「这一跳是代理坏了」，可以直连回退一次。
// 永久拒绝优先判定，避免把 token 侧拒绝误当成代理问题而白跑一次直连。
func isProxyFailure(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	if isPermanentGrantDenial(m) {
		return false
	}
	for _, s := range []string{
		"proxy authentication required",
		"407",
		"http 407",
		"connect tunnel failed",
		"proxyconnect",
		"connection refused",
		"i/o timeout",
		"timeout awaiting response headers",
		"socks connect",
		"socks5",
		"eof",
		"network is unreachable",
		"no route to host",
		"tls handshake timeout",
		"access denied",
		"http 400",
		"http 403",
		"http 502",
		"http 503",
		"http 504",
		"unauthorized", // 代理侧 401 也见过
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// isTransientConvertError 报告值得退避重试的临时错误（限流 / 网关抖动）。
func isTransientConvertError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// 永久拒绝必须先短路，否则会一直烧重试次数
	if isPermanentGrantDenial(msg) || strings.Contains(msg, "expired_token") {
		return false
	}
	for _, s := range []string{
		"http 429", "http 503", "http 502", "http 504",
		"too many requests", "rate limit",
		"context canceled", "context cancelled", "context deadline exceeded",
		"timeout",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// convertBackoff 返回第 attempt 次失败后的等待时长（与 sub2api 的 attempt²×2s 一致）。
func convertBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(attempt*attempt) * 2 * time.Second
}

// sleepCtx 可取消地睡眠；ctx 结束时立即返回其错误。
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
