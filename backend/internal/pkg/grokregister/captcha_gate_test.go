package grokregister

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetEzHostGatesForTest() {
	ezGateMu.Lock()
	ezHostGates = map[string]*ezHostGate{}
	ezGateMu.Unlock()
}

func TestEzSolverHostGatesAreIndependent(t *testing.T) {
	resetEzHostGatesForTest()
	releaseA, ok, err := acquireEzSolverHostSlot("http://a.example", 1, nil, time.Second)
	if err != nil || !ok {
		t.Fatalf("acquire host A: ok=%v err=%v", ok, err)
	}
	defer releaseA()

	if _, ok, err := acquireEzSolverHostSlot("http://a.example", 1, nil, 0); err != nil || ok {
		t.Fatalf("full host A should not acquire: ok=%v err=%v", ok, err)
	}
	releaseB, ok, err := acquireEzSolverHostSlot("http://b.example", 1, nil, 0)
	if err != nil || !ok {
		t.Fatalf("host B must remain independently available: ok=%v err=%v", ok, err)
	}
	releaseB()
}

func TestAcquireAnyEzSolverHostSkipsBusyEndpoint(t *testing.T) {
	resetEzHostGatesForTest()
	releaseA, _, err := acquireEzSolverHostSlot("http://a.example", 1, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()

	host, releaseB, err := acquireAnyEzSolverHost(
		[]string{"http://a.example", "http://b.example"}, 1, nil, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	if host != "http://b.example" {
		t.Fatalf("acquired host = %q, want available host B", host)
	}
}

func TestEzSolverHostGateWaitCancels(t *testing.T) {
	resetEzHostGatesForTest()
	release, _, err := acquireEzSolverHostSlot("http://a.example", 1, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	cancel := NewSafeCancel()
	cancel.Close()
	_, ok, err := acquireEzSolverHostSlot("http://a.example", 1, cancel, time.Second)
	if ok || err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancelled wait, ok=%v err=%v", ok, err)
	}
}

func TestRecoverableRetryRespectsHostConcurrencyGate(t *testing.T) {
	resetEzHostGatesForTest()
	var calls, active, maxActive int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cur := atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
				break
			}
		}
		n := atomic.AddInt32(&calls, 1)
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"hard-timeout 35s (CDP/page hung)"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"this-recovered-token-is-long-enough"}`))
	}))
	defer server.Close()

	cfg := EzSolverConfig{
		URL: server.URL, Nodes: []EzSolverNode{{URL: server.URL, Enabled: true}},
		TimeoutMS: 5000, MaxConcurrency: 1, Enabled: true,
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = solveTurnstileEzSolver(cfg, CaptchaOptions{EzSolverURL: server.URL},
				"https://accounts.x.ai/sign-up", "site-key", nil)
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&maxActive); got > 1 {
		t.Fatalf("retry bypassed host concurrency gate: max active=%d", got)
	}
}

func TestPauseWarmupPreservesFreshTokens(t *testing.T) {
	now := time.Now()
	ezPoolMu.Lock()
	ezPool = []ezPooledToken{{Token: "fresh-token-value", SiteKey: "key", SiteURL: "url", At: now}}
	ezPoolTarget = 6
	ezWarmPaused = false
	ezPoolMu.Unlock()

	PauseEzTokenWarmup("test")
	ezPoolMu.Lock()
	defer ezPoolMu.Unlock()
	if !ezWarmPaused {
		t.Fatal("warmup should be paused")
	}
	if len(ezPool) != 1 {
		t.Fatalf("pause discarded %d fresh tokens", 1-len(ezPool))
	}
}
