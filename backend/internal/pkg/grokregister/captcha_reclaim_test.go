package grokregister

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 回收必须按 provider 收敛：只对本批次用到的本地引擎发请求，
// 付费通道(yescaptcha/nextcaptcha)没有本地浏览器，发了纯属打扰。
func TestCaptchaEndpointsSelectsByProvider(t *testing.T) {
	base := CaptchaOptions{
		EzSolverURL: "http://127.0.0.1:8192",
		AuralithURL: "http://127.0.0.1:8194",
	}
	cases := []struct {
		provider CaptchaProvider
		wantURLs []string
	}{
		{CaptchaProviderAuralith, []string{"http://127.0.0.1:8194"}},
		{CaptchaProviderEzSolver, []string{"http://127.0.0.1:8192"}},
		{CaptchaProviderVeloraTurn, []string{"http://127.0.0.1:8192"}},
		{CaptchaProviderAuto, []string{"http://127.0.0.1:8192", "http://127.0.0.1:8194"}},
		{CaptchaProviderYesCaptcha, nil},
		{CaptchaProviderNextCaptcha, nil},
		{CaptchaProviderNone, nil},
	}
	for _, tc := range cases {
		opts := base
		opts.Provider = tc.provider
		eps := captchaEndpoints(opts)
		if len(eps) != len(tc.wantURLs) {
			t.Errorf("provider=%s: 得到 %d 个端点, want %d", tc.provider, len(eps), len(tc.wantURLs))
			continue
		}
		for i, want := range tc.wantURLs {
			if eps[i].URL != want {
				t.Errorf("provider=%s: 端点[%d]=%s, want %s", tc.provider, i, eps[i].URL, want)
			}
		}
	}
}

// 空地址不能生成端点（否则会往 "/admin/release" 这种相对路径发请求）。
func TestCaptchaEndpointsSkipsEmptyURL(t *testing.T) {
	opts := CaptchaOptions{Provider: CaptchaProviderAuralith, AuralithURL: "   "}
	if eps := captchaEndpoints(opts); len(eps) != 0 {
		t.Fatalf("空 URL 仍生成了 %d 个端点", len(eps))
	}
}

// 回收必须请求硬回收（suspend=true）+ 等待收尾（wait=true）：
// 软回收只关空闲池，warmer 会立刻把 standby 补回来，进程照样驻留。
func TestReleaseOneSendsSuspendAndWait(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"released":6,"drained":true}`))
	}))
	defer srv.Close()

	n, err := releaseOne(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("released=%d, want 6", n)
	}
	if gotPath != "/admin/release" {
		t.Fatalf("path=%s, want /admin/release", gotPath)
	}
	if gotBody["suspend"] != true {
		t.Errorf("suspend=%v, want true —— 软回收关不掉驻留进程", gotBody["suspend"])
	}
	if gotBody["wait"] != true {
		t.Errorf("wait=%v, want true —— 不等收尾会让调用方误判回收没生效", gotBody["wait"])
	}
}

// released 字段缺失时回落到 active+idle 之和（老引擎只报分项）。
func TestReleaseOneFallsBackToSplitCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"released_active":2,"released_idle":3}`))
	}))
	defer srv.Close()
	n, err := releaseOne(context.Background(), srv.URL, "")
	if err != nil || n != 5 {
		t.Fatalf("n=%d err=%v, want 5/nil", n, err)
	}
}

// 老引擎没有 /admin/release（404）时必须是「静默降级」而不是报错中断批次。
func TestReleaseOneHandles404AsSoftError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := releaseOne(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("404 应返回错误供调用方记日志")
	}
}

// 带 API Key 时必须同时发 X-API-Key 与 Bearer（不同引擎认不同头）。
func TestReleaseOneSendsBothAuthHeaders(t *testing.T) {
	var apiKey, bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("X-API-Key")
		bearer = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true,"released":0}`))
	}))
	defer srv.Close()
	if _, err := releaseOne(context.Background(), srv.URL, "secret123"); err != nil {
		t.Fatal(err)
	}
	if apiKey != "secret123" {
		t.Errorf("X-API-Key=%q", apiKey)
	}
	if bearer != "Bearer secret123" {
		t.Errorf("Authorization=%q", bearer)
	}
}

// ReleaseCaptchaBrowsers 对付费通道应当一个请求都不发。
func TestReleaseCaptchaBrowsersSkipsPaidProviders(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	ReleaseCaptchaBrowsers(context.Background(), CaptchaOptions{
		Provider:    CaptchaProviderYesCaptcha,
		EzSolverURL: srv.URL,
		AuralithURL: srv.URL,
	})
	if hits != 0 {
		t.Fatalf("付费通道发了 %d 个回收请求，应为 0", hits)
	}
}
