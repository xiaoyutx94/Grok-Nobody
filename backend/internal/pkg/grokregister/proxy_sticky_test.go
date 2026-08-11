package grokregister

import "testing"

func TestIsProxyIrrelevantFailure_CancelAndCaptcha(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"cancelled", true},
		{"canceled", true},
		{"context canceled", true},
		{"ezsolver timeout", true},
		{"Turnstile token not obtained", true},
		{"打码超时", true},
		{"socks connect failed", false},
		{"http 403 cloudflare", false},
		{"connection reset by peer", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isProxyIrrelevantFailure(c.msg); got != c.want {
			t.Fatalf("msg=%q got=%v want=%v", c.msg, got, c.want)
		}
	}
}

func TestIsUserCancelledStatus(t *testing.T) {
	if !isUserCancelledStatus("cancelled", "") {
		t.Fatal("status cancelled")
	}
	if !isUserCancelledStatus("error", "cancelled") {
		t.Fatal("error cancelled")
	}
	if !isUserCancelledStatus("error", "Turnstile 打码失败: VeloraTurn Go HTTP 503: browser release in progress") {
		t.Fatal("browser release should be cancel")
	}
	if !isUserCancelledStatus("error", "auralith HTTP 408: {\"error\":\"context canceled\"}") {
		t.Fatal("auralith context canceled should be cancel")
	}
	if isUserCancelledStatus("failed", "socks timeout") {
		t.Fatal("should not be cancel")
	}
}

func TestShouldRetryJobWithNewProxy_ProxyErrors(t *testing.T) {
	if !shouldRetryJobWithNewProxy("socks connect failed") {
		t.Fatal("socks should retry")
	}
	if !shouldRetryJobWithNewProxy("http 403") {
		t.Fatal("403 should retry")
	}
	// captcha/host solver must NOT trigger proxy switch
	if shouldRetryJobWithNewProxy("ezsolver down") {
		t.Fatal("captcha must not retry with new proxy")
	}
	if shouldRetryJobWithNewProxy("cancelled") {
		t.Fatal("cancel must not retry with new proxy")
	}
	if shouldRetryJobWithNewProxy("打码超时") {
		t.Fatal("captcha timeout must not retry with new proxy")
	}
}

// 测试用显式远程地址（源码默认不再写死远程节点）
const testRemoteURL = "http://remote-free.example:8192"

func TestEzSolverURLs_LegacyLocalNoAutoAppendRemoteByDefault(t *testing.T) {
	// 默认无远程常量：仅本机 URL 不应再自动挂远程（用户没有远程打码）
	urls := ezSolverURLs(EzSolverConfig{URL: "http://172.18.0.1:8192"}, CaptchaOptions{})
	if len(urls) != 1 || urls[0] != "http://172.18.0.1:8192" {
		t.Fatalf("默认不应自动附加远程: %#v", urls)
	}
	// 显式远程-only 应保持单节点
	urls2 := ezSolverURLs(EzSolverConfig{URL: testRemoteURL}, CaptchaOptions{})
	if len(urls2) != 1 || urls2[0] != testRemoteURL {
		t.Fatalf("remote-only should stay single: %#v", urls2)
	}
	// 显式双节点保持双节点（不去重成单）
	urls3 := ezSolverURLs(EzSolverConfig{URL: "http://172.18.0.1:8192," + testRemoteURL}, CaptchaOptions{})
	if len(urls3) != 2 {
		t.Fatalf("dual list should be 2 unique: %#v", urls3)
	}
}

func TestEzSolverURLs_ExplicitNodesAreAuthoritative(t *testing.T) {
	local := "http://172.18.0.1:8192"
	cfg := EzSolverConfig{
		URL: "http://172.18.0.1:8192," + testRemoteURL,
		Nodes: []EzSolverNode{
			{Name: "local", URL: local, Enabled: true},
			{Name: "remote", URL: testRemoteURL, Enabled: false},
		},
	}
	urls := ezSolverURLs(cfg, CaptchaOptions{EzSolverURL: "http://172.18.0.1:8192," + testRemoteURL})
	if len(urls) != 1 || urls[0] != local {
		t.Fatalf("explicit local-only selection must stay local-only: %#v", urls)
	}

	cfg.Nodes[0].Enabled = false
	cfg.Nodes[1].Enabled = true
	urls = ezSolverURLs(cfg, CaptchaOptions{})
	if len(urls) != 1 || urls[0] != testRemoteURL {
		t.Fatalf("explicit remote-only selection must stay remote-only: %#v", urls)
	}
}
