package grokregister

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRewriteAuralithProxyURL(t *testing.T) {
	rewrites := map[string]string{
		"socks5://warp-2:1080": "socks5h://127.0.0.1:10802",
	}
	tests := []struct {
		name      string
		proxyURL  string
		want      string
		rewritten bool
	}{
		{name: "exact", proxyURL: "socks5://warp-2:1080", want: "socks5h://127.0.0.1:10802", rewritten: true},
		{name: "whitespace", proxyURL: "  socks5://warp-2:1080\n", want: "socks5h://127.0.0.1:10802", rewritten: true},
		{name: "no match", proxyURL: "socks5://WARP-2:1080", want: "socks5://WARP-2:1080", rewritten: false},
		{name: "empty", proxyURL: "  ", want: "", rewritten: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, rewritten := rewriteAuralithProxyURL(tc.proxyURL, rewrites)
			if got != tc.want || rewritten != tc.rewritten {
				t.Fatalf("rewriteAuralithProxyURL() = (%q, %v), want (%q, %v)", got, rewritten, tc.want, tc.rewritten)
			}
		})
	}
}

func TestAuralithProxyRewriteIsRuntimeOnly(t *testing.T) {
	raw, err := json.Marshal(AuralithConfig{
		URL:          "http://127.0.0.1:8194",
		ProxyRewrite: map[string]string{"socks5://source.example:1080": "socks5h://target.example:1080"},
	})
	if err != nil {
		t.Fatalf("marshal AuralithConfig: %v", err)
	}
	if bytes.Contains(raw, []byte("ProxyRewrite")) || bytes.Contains(raw, []byte("source.example")) || bytes.Contains(raw, []byte("target.example")) {
		t.Fatalf("runtime proxy rewrite was serialized: %s", raw)
	}
}

func TestAuralithProviderRewritesOnlySolvePayloadProxy(t *testing.T) {
	const (
		source = "socks5://source-user:source-pass@warp-2:1080"
		target = "socks5h://target-user:target-pass@127.0.0.1:10802"
	)
	previous := GetCaptchaRuntime()
	t.Cleanup(func() { SetCaptchaRuntime(previous) })

	var rawBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/solve" {
			t.Fatalf("path=%q, want /solve", r.URL.Path)
		}
		var err error
		rawBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "auralith-token-long-enough-for-test"})
	}))
	defer ts.Close()

	SetCaptchaRuntime(CaptchaRuntime{
		DefaultProvider: CaptchaProviderAuralith,
		Auralith: AuralithConfig{
			URL:            ts.URL,
			TimeoutMS:      5000,
			MaxConcurrency: 1,
			Enabled:        true,
			ProxyRewrite:   map[string]string{source: target},
		},
		AuralithRetry: 1,
		SolveViaProxy: true,
	})
	res, err := SolveTurnstileWithOptions(CaptchaOptions{
		Provider: CaptchaProviderAuralith,
		ProxyURL: "  " + source + "  ",
		SiteURL:  "https://example.test",
		SiteKey:  "site-key",
	}, nil)
	if err != nil || res == nil {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got, _ := payload["proxy"].(string); got != target {
		t.Fatalf("payload proxy=%q, want rewritten target", got)
	}
	if bytes.Contains(rawBody, []byte(source)) {
		t.Fatal("payload retained source proxy")
	}
}

func TestAuralithProviderUsesIndependentRuntimeAndProxy(t *testing.T) {
	var proxy string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		proxy, _ = body["proxy"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "auralith-token-long-enough-for-test"})
	}))
	defer ts.Close()
	SetCaptchaRuntime(CaptchaRuntime{DefaultProvider: CaptchaProviderAuralith, EzSolver: EzSolverConfig{URL: "http://must-not-be-used.invalid", Enabled: true}, Auralith: AuralithConfig{URL: ts.URL, TimeoutMS: 5000, MaxConcurrency: 1, Enabled: true}, AuralithRetry: 1, SolveViaProxy: true})
	res, err := SolveTurnstileWithOptions(CaptchaOptions{Provider: CaptchaProviderAuralith, ProxyURL: "http://proxy.example:8080", SiteURL: "https://example.test", SiteKey: "site-key"}, nil)
	if err != nil || res == nil || res.Provider != CaptchaProviderAuralith {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if proxy != "http://proxy.example:8080" {
		t.Fatalf("proxy=%q", proxy)
	}
}

func TestAuralithDoesNotJoinAutoProviderOrder(t *testing.T) {
	SetCaptchaRuntime(CaptchaRuntime{DefaultProvider: CaptchaProviderAuto, EzSolver: EzSolverConfig{URL: "", Enabled: false}, Auralith: AuralithConfig{URL: "http://auralith.invalid", Enabled: true}})
	_, err := SolveTurnstileWithOptions(CaptchaOptions{Provider: CaptchaProviderAuto}, nil)
	if err == nil || !strings.Contains(err.Error(), "无可用收费") {
		t.Fatalf("auto unexpectedly used Auralith: %v", err)
	}
}

func TestAuralithEnabledNodeInheritsGlobalAPIKey(t *testing.T) {
	var gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "auralith-token-long-enough-for-test"})
	}))
	defer ts.Close()

	SetCaptchaRuntime(CaptchaRuntime{
		DefaultProvider: CaptchaProviderAuralith,
		Auralith: AuralithConfig{
			URL:            ts.URL,
			APIKey:         "global-secret",
			Nodes:          []AuralithNode{{URL: ts.URL, Enabled: true}},
			TimeoutMS:      5000,
			MaxConcurrency: 1,
			Enabled:        true,
		},
	})
	res, err := SolveTurnstileWithOptions(CaptchaOptions{
		Provider: CaptchaProviderAuralith,
		SiteURL:  "https://example.test",
		SiteKey:  "site-key",
	}, nil)
	if err != nil || res == nil || res.Token == "" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if gotKey != "global-secret" {
		t.Fatalf("X-Api-Key=%q, want inherited global key", gotKey)
	}
}
