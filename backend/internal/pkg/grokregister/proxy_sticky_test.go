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

func TestEzSolverURLs_LegacyLocalAutoAppendsRemote(t *testing.T) {
	urls := ezSolverURLs(EzSolverConfig{URL: "http://172.18.0.1:8192"}, CaptchaOptions{})
	if len(urls) < 2 {
		t.Fatalf("expected dual hosts, got %#v", urls)
	}
	foundRemote := false
	for _, u := range urls {
		if u == DefaultRemoteEzSolverURL {
			foundRemote = true
		}
	}
	if !foundRemote {
		t.Fatalf("remote not appended: %#v", urls)
	}
	// remote-only should not duplicate local
	urls2 := ezSolverURLs(EzSolverConfig{URL: DefaultRemoteEzSolverURL}, CaptchaOptions{})
	if len(urls2) != 1 || urls2[0] != DefaultRemoteEzSolverURL {
		t.Fatalf("remote-only should stay single: %#v", urls2)
	}
	// already dual should stay dual (no third)
	urls3 := ezSolverURLs(EzSolverConfig{URL: DefaultDualEzSolverURL}, CaptchaOptions{})
	if len(urls3) != 2 {
		t.Fatalf("dual list should be 2 unique: %#v", urls3)
	}
}

func TestEzSolverURLs_ExplicitNodesAreAuthoritative(t *testing.T) {
	local := "http://172.18.0.1:8192"
	cfg := EzSolverConfig{
		URL: DefaultDualEzSolverURL,
		Nodes: []EzSolverNode{
			{Name: "local", URL: local, Enabled: true},
			{Name: "ARM", URL: DefaultRemoteEzSolverURL, Enabled: false},
		},
	}
	urls := ezSolverURLs(cfg, CaptchaOptions{EzSolverURL: DefaultDualEzSolverURL})
	if len(urls) != 1 || urls[0] != local {
		t.Fatalf("explicit local-only selection must stay local-only: %#v", urls)
	}

	cfg.Nodes[0].Enabled = false
	cfg.Nodes[1].Enabled = true
	urls = ezSolverURLs(cfg, CaptchaOptions{})
	if len(urls) != 1 || urls[0] != DefaultRemoteEzSolverURL {
		t.Fatalf("explicit remote-only selection must stay remote-only: %#v", urls)
	}
}
