package captcha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuralithClientSolveForwardsProxyAndAuth(t *testing.T) {
	var gotProxy, gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotProxy, _ = body["proxy"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "auralith-token-long-enough-for-test"})
	}))
	defer ts.Close()

	c := NewAuralithClient(ts.URL, 10, "secret")
	tok, err := c.Solve(context.Background(), "site-key", "https://example.test", "socks5://user:pass@proxy:1080")
	if err != nil || tok == "" {
		t.Fatalf("solve token=%q err=%v", tok, err)
	}
	if gotProxy != "socks5://user:pass@proxy:1080" {
		t.Fatalf("proxy=%q", gotProxy)
	}
	if gotKey != "secret" {
		t.Fatalf("X-Api-Key=%q", gotKey)
	}
}

func TestAuralithClientAdminEndpoints(t *testing.T) {
	seen := map[string]int{}
	releaseWait := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path]++
		if r.URL.Path == "/admin/release" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			releaseWait, _ = body["wait"].(bool)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "ok", "solve_ready": true, "workers": 3, "released": 2})
	}))
	defer ts.Close()
	c := NewAuralithClient(ts.URL, 10, "")
	if !c.Health(context.Background()).OK || !c.Config(context.Background()).OK || !c.Release(context.Background()).OK {
		t.Fatal("expected health/config/release success")
	}
	for _, key := range []string{"GET /health", "GET /admin/config", "POST /admin/release"} {
		if seen[key] != 1 {
			t.Fatalf("%s calls=%d", key, seen[key])
		}
	}
	if releaseWait {
		t.Fatal("release request must be non-blocking (wait=false) so Stop cannot freeze the host")
	}
}

func TestAuralithHealthPreservesRealSnapshotAndReadiness(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "degraded", "browser_ready": true, "context_ready": true, "oopif_ready": false, "proxy_ready": true, "solve_ready": false,
			"metrics":      map[string]any{"phases": map[string]any{"solve": map[string]any{"count": 2, "p50_ms": 12.5, "p95_ms": 20.0}}},
			"resource":     map[string]any{"rss_mb": 321, "go_heap_mb": 22, "cpu_percent": 44.5},
			"context_pool": map[string]any{"mode": "context", "live_processes": 2, "active_contexts": 1, "fallbacks": 0},
			"budget":       map[string]any{"mode": "manual", "max_browsers": 2, "max_contexts_per_browser": 4, "cpu_high_water": 85, "rss_high_water_mb": 1024, "queue_timeout": 15000000000},
		})
	}))
	defer ts.Close()
	snap := NewAuralithClient(ts.URL, 10, "").Health(context.Background())
	if snap.OK || snap.SolveReady || snap.Status != "degraded" {
		t.Fatalf("readiness=%+v", snap)
	}
	if snap.Resource.RSSMB != 321 || snap.ContextPool.LiveProcesses != 2 || snap.ContextPool.Fallbacks != 0 || snap.Metrics.Phases["solve"].P95MS != 20 || snap.Budget.MaxContextsPerBrowser != 4 {
		t.Fatalf("snapshot fields lost: %+v", snap)
	}
}

func TestNewAuralithClientRejectsUnsafeBaseURLs(t *testing.T) {
	for _, raw := range []string{"ftp://127.0.0.1:8194", "http://user:pass@127.0.0.1:8194", "http://169.254.169.254/latest", "http://8.8.8.8:8194"} {
		if _, err := NewAuralithClientValidated(raw, 10, ""); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8194", "http://172.18.0.1:8194", "https://auralith.example.com"} {
		if _, err := NewAuralithClientValidated(raw, 10, ""); err != nil {
			t.Fatalf("safe management URL rejected %s: %v", raw, err)
		}
	}
}
