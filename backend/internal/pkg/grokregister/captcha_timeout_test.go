package grokregister

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostEzSolverOnceHonorsRemainingBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"this-token-is-long-enough-for-the-test"}`))
	}))
	defer server.Close()

	started := time.Now()
	_, err := postEzSolverOnce(
		server.URL, "", "https://grok.com/sign-up", "site-key", 10,
		40*time.Millisecond, nil, "", CaptchaProviderEzSolver,
	)
	if err == nil {
		t.Fatal("expected request deadline error")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("request exceeded remaining budget: %s", elapsed)
	}
}

func TestEzSolverAttemptTimeoutUsesNormalChallengeWindow(t *testing.T) {
	if got := ezSolverAttemptTimeout(2 * time.Minute); got != 35 {
		t.Fatalf("attempt timeout=%d want=35", got)
	}
	if got := ezSolverAttemptTimeout(20 * time.Second); got != 20 {
		t.Fatalf("remaining-bound timeout=%d want=20", got)
	}
}

func TestShouldRetryRecoverableEzSolverError(t *testing.T) {
	for _, msg := range []string{
		"EzSolver HTTP 500: hard-timeout 35s (CDP/page hung)",
		"soft-timeout 28s (slow challenge; recycle worker)",
		"browser disconnected",
	} {
		if !shouldRetryEzSolverError(fmt.Errorf("%s", msg)) {
			t.Fatalf("expected recoverable: %s", msg)
		}
	}
	for _, msg := range []string{
		"EzSolver HTTP 401: unauthorized",
		"EzSolver URL 为空",
		"connection refused",
	} {
		if shouldRetryEzSolverError(fmt.Errorf("%s", msg)) {
			t.Fatalf("must fail fast: %s", msg)
		}
	}
}

func TestSolveTurnstileEzSolverRetriesRecoverableHostOnce(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"hard-timeout 35s (CDP/page hung)"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"this-recovered-token-is-long-enough"}`))
	}))
	defer server.Close()

	res, err := solveTurnstileEzSolver(EzSolverConfig{
		URL: server.URL, Nodes: []EzSolverNode{{URL: server.URL, Enabled: true}}, TimeoutMS: 20000, MaxConcurrency: 1, Enabled: true,
	}, CaptchaOptions{EzSolverURL: server.URL}, "https://accounts.x.ai/sign-up", "site-key", nil)
	if err != nil {
		t.Fatalf("recoverable solve failed: %v", err)
	}
	if res == nil || res.Token == "" {
		t.Fatal("expected recovered token")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls=%d want=2", got)
	}
}
