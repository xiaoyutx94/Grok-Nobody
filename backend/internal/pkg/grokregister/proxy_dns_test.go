package grokregister

import "testing"

func TestIsProxyDNSFailure(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		// DNS 解析失败 → 代理出口故障,必须淘汰
		{`Turnstile 打码失败: auralith HTTP 400: {"error":"target host could not be safely resolved"}`, true},
		{"auralith request: Post \"http://127.0.0.1:8194/solve\": no such host", true},
		{"lookup accounts.x.ai on 8.8.8.8:53: no such host", true},
		{"dial tcp: lookup x.ai: server misbehaving", true},
		{"temporary failure in name resolution", true},
		{"nodename nor servname provided, or not known", true},
		// 非 DNS 类 → 走原有分类
		{"Turnstile 打码失败: auralith HTTP 408 timeout", false},
		{"captcha solve timeout", false},
		{"http 429 rate limit", false},
		{"cancelled", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isProxyDNSFailure(c.err); got != c.want {
			t.Errorf("isProxyDNSFailure(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}
