package xai

import (
	"strconv"
	"strings"
	"time"
)

// 用量/配额观察（复刻 sub2api 的 QuotaSnapshot 语义）：
// xAI 在真实对话响应头里下发 x-ratelimit-* 配额（token/请求窗口），
// 免费账号典型值：tokens 1,000,000（24h 滚动），requests 21。
// 限流/额度耗尽信号：429 + Retry-After / x-ratelimit-reset，或响应体 error_code。

const GrokFreeRolling24hTokenLimit int64 = 1_000_000

// IsGrokFreeRolling24hTokenLimit 判断是否为免费号 24h 滚动 token 配额。
func IsGrokFreeRolling24hTokenLimit(limit int64) bool {
	return limit == GrokFreeRolling24hTokenLimit || limit == 2_000_000 // 2M 为 2026-07 前的旧免费窗口
}

// QuotaWindow 一个配额窗口（requests 或 tokens）。
type QuotaWindow struct {
	Limit     *int64 `json:"limit,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
	ResetUnix *int64 `json:"reset_unix,omitempty"`
	ResetAt   string `json:"reset_at,omitempty"`
}

// QuotaSnapshot 一次响应头观察的完整配额快照。
type QuotaSnapshot struct {
	Requests *QuotaWindow `json:"requests,omitempty"`
	Tokens   *QuotaWindow `json:"tokens,omitempty"`
	// ErrorCode / ErrorMessage 捕获 xAI 放在响应体里的额度耗尽信号
	// （而非 Retry-After / x-ratelimit-reset 头）。
	ErrorCode         string            `json:"error_code,omitempty"`
	ErrorMessage      string            `json:"error_message,omitempty"`
	RetryAfterSeconds *int              `json:"retry_after_seconds,omitempty"`
	SubscriptionTier  string            `json:"subscription_tier,omitempty"`
	EntitlementStatus string            `json:"entitlement_status,omitempty"`
	StatusCode        int               `json:"status_code,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	HeadersObserved   bool              `json:"headers_observed"`
	ObservationSource string            `json:"observation_source,omitempty"`
	LastProbeAt       string            `json:"last_probe_at,omitempty"`
	UpdatedAt         string            `json:"updated_at"`
}

// ParseQuotaSnapshot 从响应头解析配额快照（x-ratelimit-* / Retry-After / x-ratelimit-reset）。
func ParseQuotaSnapshot(headers map[string][]string, statusCode int, source string) *QuotaSnapshot {
	if len(headers) == 0 {
		return nil
	}
	snap := &QuotaSnapshot{
		StatusCode:        statusCode,
		ObservationSource: source,
		HeadersObserved:   true,
		Headers:           make(map[string]string, len(headers)),
		LastProbeAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	for k, vs := range headers {
		if len(vs) > 0 {
			snap.Headers[strings.ToLower(k)] = vs[0]
		}
	}
	get := func(name string) string { return snap.Headers[strings.ToLower(name)] }
	atoi := func(s string) *int64 {
		if s == "" {
			return nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	atoi32 := func(s string) *int {
		if s == "" {
			return nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil
		}
		return &n
	}

	// requests 窗口
	if v := get("x-ratelimit-limit-requests"); v != "" {
		snap.Requests = &QuotaWindow{Limit: atoi(v)}
		if r := atoi(get("x-ratelimit-remaining-requests")); r != nil {
			snap.Requests.Remaining = r
		}
		if rs := get("x-ratelimit-reset-requests"); rs != "" {
			if sec := atoi(rs); sec != nil {
				snap.Requests.ResetUnix = sec
				snap.Requests.ResetAt = time.Now().UTC().Add(time.Duration(*sec) * time.Second).Format(time.RFC3339)
			}
		}
	}
	// tokens 窗口
	if v := get("x-ratelimit-limit-tokens"); v != "" {
		snap.Tokens = &QuotaWindow{Limit: atoi(v)}
		if r := atoi(get("x-ratelimit-remaining-tokens")); r != nil {
			snap.Tokens.Remaining = r
		}
		if rs := get("x-ratelimit-reset-tokens"); rs != "" {
			if sec := atoi(rs); sec != nil {
				snap.Tokens.ResetUnix = sec
				snap.Tokens.ResetAt = time.Now().UTC().Add(time.Duration(*sec) * time.Second).Format(time.RFC3339)
			}
		}
	}
	// 限流
	if ra := get("retry-after"); ra != "" {
		snap.RetryAfterSeconds = atoi32(ra)
	}
	if rs := get("x-ratelimit-reset"); rs != "" {
		if sec := atoi(rs); sec != nil && snap.RetryAfterSeconds == nil {
			snap.RetryAfterSeconds = atoi32(rs)
		}
	}
	return snap
}

// TokenRemainingPercent tokens 剩余百分比（0-100）。
func (s *QuotaSnapshot) TokenRemainingPercent() float64 {
	if s == nil || s.Tokens == nil || s.Tokens.Limit == nil || *s.Tokens.Limit <= 0 || s.Tokens.Remaining == nil {
		return 100
	}
	pct := float64(*s.Tokens.Remaining) * 100 / float64(*s.Tokens.Limit)
	if pct < 0 {
		return 0
	}
	return pct
}

// IsRateLimited 是否处于限流（Retry-After 或 429 + reset）。
func (s *QuotaSnapshot) IsRateLimited() bool {
	return s != nil && s.RetryAfterSeconds != nil && *s.RetryAfterSeconds > 0
}
