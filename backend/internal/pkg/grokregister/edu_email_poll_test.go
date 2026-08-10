package grokregister

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Local strategy: push cache is checked immediately; Worker /api/otp is only
// used as remote fallback after ~2s. Interval no longer drives remote polling.
func TestCFWorkerWaitForCodeRemoteFallbackAfterFiveSeconds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/otp" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "code": "123456"})
	}))
	defer server.Close()

	email := NewCFWorkerEmail("example.edu", server.URL, "", "", "", "")
	email.address = "student@example.edu"
	started := time.Now()
	// Timeout must exceed the 5s remote-fallback gate; interval is ignored for remote.
	code, err := email.WaitForCode(8*time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if code != "123456" {
		t.Fatalf("code=%q want=123456", code)
	}
	if calls.Load() < 1 {
		t.Fatalf("worker API calls=%d want>=1 after remote fallback", calls.Load())
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Fatalf("remote fallback started too early: %s (want >=2s)", elapsed)
	}
}

func TestCFWorkerCallbackWakesActiveMailboxWithoutPolling(t *testing.T) {
	email := NewCFWorkerEmail("example.edu", "http://127.0.0.1:1", "", "", "", "")
	address, err := email.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !StorePushedOTP(address, "654321", "") {
		t.Fatal("callback for active mailbox was rejected")
	}
	code, err := email.WaitForCode(time.Second, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if code != "654321" {
		t.Fatalf("code=%q want=654321", code)
	}
	if StorePushedOTP(address, "111111", "") {
		t.Fatal("callback after mailbox completion must be rejected")
	}
}

func TestCFWorkerPushCacheSurvivesFailedExtract(t *testing.T) {
	email := NewCFWorkerEmail("example.edu", "http://127.0.0.1:1", "", "", "", "")
	address, err := email.Create()
	if err != nil {
		t.Fatal(err)
	}
	// Structured Grok code + noisy body that would previously be preferred and
	// could fail extract depending on extractor; reject-all first then accept.
	if !StorePushedOTP(address, "VOL-USX", "noise without code") {
		t.Fatal("store")
	}
	rejects := 0
	email.SetCodeExtractor(func(text string) string {
		if strings.Contains(text, "VOL-USX") || strings.Contains(text, "VOLUSX") {
			if rejects < 1 {
				rejects++
				return "" // first pass: pretend extract not ready
			}
			return strings.ReplaceAll(strings.ToUpper(text), "-", "")
		}
		return ""
	})
	// Force one failed extract without deleting cache via direct helper path.
	if got := email.extractOTPCode(kvResult{Code: "VOL-USX", Body: "noise"}); got != "" {
		t.Fatalf("first extract should fail, got %q", got)
	}
	if _, ok := otpCache.Load(strings.ToLower(address)); !ok {
		t.Fatal("failed extract must not drop push cache")
	}
	// Prefer structured code over body false-positive.
	email.SetCodeExtractor(func(text string) string {
		if text == "VOL-USX" {
			return "VOLUSX"
		}
		if strings.Contains(text, "BAD-BAD") {
			return "BADBAD"
		}
		return ""
	})
	if got := email.extractOTPCode(kvResult{Code: "VOL-USX", Body: "see BAD-BAD in footer"}); got != "VOLUSX" {
		t.Fatalf("prefer structured code, got %q", got)
	}
	code, err := email.WaitForCode(time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if code != "VOLUSX" {
		t.Fatalf("code=%q", code)
	}
}
