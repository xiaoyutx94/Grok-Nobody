package grokregister

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Local strategy: push cache spins immediately; remote fallback begins after ~5s.
func TestCFWorkerLocalStrategyDoesNotPollRemoteBeforeFiveSeconds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"found": false})
	}))
	defer srv.Close()
	email := NewCFWorkerEmail("example.com", srv.URL, "", "", "", "")
	email.address = "student@example.com"
	_, err := email.WaitForCode(1200*time.Millisecond, 250*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("remote polls before 5s = %d, want 0", got)
	}
}

// With KV credentials, the local implementation uses direct KV as the sole remote
// fallback after 5s; Worker API is not also polled.
func TestCFWorkerLocalStrategyKVPreferredOverWorker(t *testing.T) {
	var workerCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "code": "BAD-BAD"})
	}))
	defer srv.Close()
	email := NewCFWorkerEmail("example.com", srv.URL, "key", "acct", "token", "namespace")
	email.address = "student@example.com"
	// Push hit avoids waiting 5s while still proving Worker is not touched when KV exists.
	registerOTPPending(email.address)
	if !StorePushedOTP(email.address, "ABC-DEF", "") {
		t.Fatal("push store")
	}
	email.SetCodeExtractor(func(s string) string {
		if s == "ABC-DEF" {
			return "ABCDEF"
		}
		return ""
	})
	code, err := email.WaitForCode(time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if code != "ABCDEF" {
		t.Fatalf("code=%q", code)
	}
	if got := workerCalls.Load(); got != 0 {
		t.Fatalf("Worker calls with KV configured = %d, want 0", got)
	}
}

// Push must be checked immediately; the requested remote interval must not delay it.
func TestCFWorkerLocalStrategyPushIsImmediate(t *testing.T) {
	email := NewCFWorkerEmail("example.com", "http://127.0.0.1:1", "", "", "", "")
	email.address = "student@example.com"
	registerOTPPending(email.address)
	if !StorePushedOTP(email.address, "VOL-USX", "") {
		t.Fatal("push store")
	}
	email.SetCodeExtractor(func(s string) string {
		if s == "VOL-USX" {
			return "VOLUSX"
		}
		return ""
	})
	started := time.Now()
	code, err := email.WaitForCode(2*time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != "VOLUSX" {
		t.Fatalf("code=%q", code)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("push hit took %v, want <=400ms", elapsed)
	}
}
