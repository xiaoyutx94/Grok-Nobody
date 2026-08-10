package grokregister

import "testing"

// 真实日志里的 region 403 串(x.ai 对被封地区出口的响应)。
const region403Err = `发送验证码失败: 发送验证码返回 403: <!doctype html>
<html>
<body>
<p>This service is not available in your region.</p>
<script>(function`

func TestRegionBlockedTriggersProxyRetry(t *testing.T) {
	// 回归点:此前 keys 只有 "http 403",中文串「返回 403」命中不到,
	// region 封锁的出口不换代理 → 首个账号直接判失败。
	if !shouldRetryJobWithNewProxy(region403Err) {
		t.Fatal("region 403 必须触发换代理重试")
	}
	if !isRegionBlockedFailure(region403Err) {
		t.Fatal("region 403 必须被判为地区封锁")
	}
	// 不能被误判成打码无关(否则不计代理失败、不冷却)
	if isProxyIrrelevantFailure(region403Err) {
		t.Fatal("region 403 不该被判为「与代理无关」")
	}
}

func TestIsRegionBlockedFailure(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{region403Err, true},
		{"This service is not available in your region.", true},
		{"service unavailable in your region", true},
		{"blocked in your country", true},
		// 非地区类
		{"发送验证码返回 429: too many requests", false},
		{"auralith HTTP 408 timeout", false},
		{"cancelled", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isRegionBlockedFailure(c.err); got != c.want {
			t.Errorf("isRegionBlockedFailure(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestOther403FormsRetry(t *testing.T) {
	// 各种 403/451 形态都应换出口重试
	for _, e := range []string{
		"访问注册页面失败: HTTP 403",
		"发送验证码返回 403: forbidden",
		"初始化返回 451: unavailable for legal reasons",
		"发送验证码返回 451: blocked",
	} {
		if !shouldRetryJobWithNewProxy(e) {
			t.Errorf("应换代理重试: %q", e)
		}
	}
}
