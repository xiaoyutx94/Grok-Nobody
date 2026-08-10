package grokregister

import (
	"testing"
	"time"
)

// TestIsRateLimitFailureDetectsUpstreamThrottle 限流必须能被显式识别出来。
// 这直接决定处置方式：限流 → 冷却避让（代理还能用）；其它失败 → 淘汰或重连。
// 此前没有这个判定，429 只能靠通用模式偶然命中，日志里也分不清两者。
func TestIsRateLimitFailureDetectsUpstreamThrottle(t *testing.T) {
	rateLimited := []string{
		"http 429 too many requests",
		"注册提交失败: status 429",
		"rate limit exceeded",
		"rate-limited by upstream",
		"Too Many Requests",
		"请求过于频繁，请稍后再试",
		"上游频率限制",
		"访问过于频繁",
		"提交注册失败 429",
	}
	for _, msg := range rateLimited {
		if !isRateLimitFailure(msg) {
			t.Errorf("isRateLimitFailure(%q) = false, want true", msg)
		}
	}

	notRateLimited := []string{
		"",
		"socks5 认证失败",
		"connection reset by peer",
		"i/o timeout",
		"cloudflare challenge",
		"http 403 forbidden",
		"未获取到 sso",
		// 关键反例：耗时/字节数里出现 429 不能误判成限流
		"注册耗时 4291ms",
		"响应 42900 字节",
	}
	for _, msg := range notRateLimited {
		if isRateLimitFailure(msg) {
			t.Errorf("isRateLimitFailure(%q) = true, want false", msg)
		}
	}
}

// TestRateLimitNeverStealsCaptchaFailures 打码侧失败优先级必须高于限流判定：
// 打码服务自己返回 429 是打码的问题，不是注册出口被限流。
// 若判错，会把好代理挂起冷却，而真正的打码故障被掩盖。
func TestIsRateLimitFailureYieldsToCaptchaFailures(t *testing.T) {
	captchaWith429 := []string{
		"ezsolver http 429",
		"auralith rate limit",
		"打码超时 429",
		"turnstile too many requests",
		"veloraturn go http 503",
	}
	for _, msg := range captchaWith429 {
		if !isProxyIrrelevantFailure(msg) {
			t.Fatalf("前置条件不成立：%q 应先被判为打码侧失败", msg)
		}
		if isRateLimitFailure(msg) {
			t.Errorf("isRateLimitFailure(%q) = true —— 打码侧失败不能被当成代理限流，"+
				"否则会白挂起一个好代理", msg)
		}
	}
}

// TestSoonestCoolingIndexNeverStarvesPool 安全不变式：整池都在冷却时必须仍能
// 选出一个代理。选不出来 → pickProxy 返回空串 → worker 走无代理直连，
// 用本机真实 IP 裸奔注册。这比撞限流严重得多，所以宁可继续用冷却中的代理。
func TestSoonestCoolingIndexNeverStarvesPool(t *testing.T) {
	now := time.Now()
	pool := []string{"socks5://a:1080", "socks5://b:1080", "socks5://c:1080"}

	t.Run("整池冷却时选最早到期的", func(t *testing.T) {
		cool := map[string]time.Time{
			pool[0]: now.Add(9 * time.Minute),
			pool[1]: now.Add(2 * time.Minute), // 最早到期
			pool[2]: now.Add(5 * time.Minute),
		}
		idx, until, all := soonestCoolingIndex(pool, cool, now)
		if !all {
			t.Fatal("allCooling = false，应为 true")
		}
		if idx != 1 {
			t.Errorf("idx = %d, want 1（最早到期的那个）", idx)
		}
		if !until.Equal(cool[pool[1]]) {
			t.Errorf("until = %v, want %v", until, cool[pool[1]])
		}
	})

	t.Run("有可用代理时交回正常路径", func(t *testing.T) {
		cool := map[string]time.Time{
			pool[0]: now.Add(9 * time.Minute),
			// pool[1] 未冷却
			pool[2]: now.Add(5 * time.Minute),
		}
		if _, _, all := soonestCoolingIndex(pool, cool, now); all {
			t.Error("有未冷却代理时 allCooling 必须为 false")
		}
	})

	t.Run("冷却已到期视为可用", func(t *testing.T) {
		cool := map[string]time.Time{
			pool[0]: now.Add(-1 * time.Minute), // 已过期
			pool[1]: now.Add(5 * time.Minute),
			pool[2]: now.Add(5 * time.Minute),
		}
		if _, _, all := soonestCoolingIndex(pool, cool, now); all {
			t.Error("已到期的冷却必须视为可用，不该报 allCooling")
		}
	})

	t.Run("空池不panic", func(t *testing.T) {
		if idx, _, all := soonestCoolingIndex(nil, map[string]time.Time{}, now); all || idx != -1 {
			t.Errorf("空池应返回 idx=-1, allCooling=false，得到 idx=%d all=%v", idx, all)
		}
	})
}

// TestProxyIsCoolingBoundary 冷却判定的时间边界：到期那一刻即视为可用，
// 不能因为 <= 写成 < 而让代理多挂一轮。
func TestProxyIsCoolingBoundary(t *testing.T) {
	now := time.Now()
	p := "socks5://x:1080"
	if proxyIsCooling(map[string]time.Time{}, p, now) {
		t.Error("没有冷却记录的代理不该判为冷却中")
	}
	if !proxyIsCooling(map[string]time.Time{p: now.Add(time.Second)}, p, now) {
		t.Error("未到期应判为冷却中")
	}
	if proxyIsCooling(map[string]time.Time{p: now}, p, now) {
		t.Error("到期那一刻应视为可用")
	}
	if proxyIsCooling(map[string]time.Time{p: now.Add(-time.Second)}, p, now) {
		t.Error("已过期应视为可用")
	}
}

// TestDefaultBatchConfigHasCooldown 默认必须开着冷却：这是用户明确要的行为
// （连续失败 3 次后自动换代理），默认关掉等于功能不存在。
func TestDefaultBatchConfigHasCooldown(t *testing.T) {
	def := DefaultBatchConfig()
	if def.MaxProxyFails != 3 {
		t.Errorf("MaxProxyFails = %d, want 3", def.MaxProxyFails)
	}
	if def.ProxyCooldownMinutes <= 0 {
		t.Errorf("ProxyCooldownMinutes = %d, want >0（默认必须启用冷却）", def.ProxyCooldownMinutes)
	}
}
