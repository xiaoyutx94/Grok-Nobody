package grokregister

import (
	"sync"
	"testing"
	"time"
)

func clearOTPStateForTest(email string) {
	otpLifecycleMu.Lock()
	defer otpLifecycleMu.Unlock()
	for _, k := range otpEmailAliasKeys(email) {
		otpPending.Delete(k)
		otpCache.Delete(k)
		delete(otpCompleted, k)
	}
}

func TestOTPEmailAliasKeys(t *testing.T) {
	keys := otpEmailAliasKeys("User@Example.com")
	if len(keys) != 2 || keys[0] != "user@example.com" || keys[1] != "user@edu.example.com" {
		t.Fatalf("root dual keys=%v", keys)
	}
	keys2 := otpEmailAliasKeys("user@edu.example.com")
	if len(keys2) != 2 || keys2[0] != "user@edu.example.com" || keys2[1] != "user@example.com" {
		t.Fatalf("edu dual keys=%v", keys2)
	}
	// no double-strip of non-edu multi-label
	keys3 := otpEmailAliasKeys("a@mail.example.com")
	if len(keys3) != 2 || keys3[1] != "a@edu.mail.example.com" {
		t.Fatalf("non-edu multi-label keys=%v", keys3)
	}
}

func TestStorePushedOTP_RootEduDualFormLookup(t *testing.T) {
	root := "alice@example.com"
	edu := "alice@edu.example.com"
	clearOTPStateForTest(root)
	t.Cleanup(func() { clearOTPStateForTest(root) })

	// Store under bare root; waiter on edu form must hit.
	if !StorePushedOTP(root, "ROOT-OTP", "") {
		t.Fatal("store root")
	}
	entry, ok := loadOTPCacheEntry(edu)
	if !ok || entry.Code != "ROOT-OTP" {
		t.Fatalf("edu load after root store: ok=%v entry=%+v", ok, entry)
	}

	clearOTPStateForTest(root)
	// Store under edu; waiter on bare root must hit.
	if !StorePushedOTP(edu, "EDU-OTP", "") {
		t.Fatal("store edu")
	}
	entry, ok = loadOTPCacheEntry(root)
	if !ok || entry.Code != "EDU-OTP" {
		t.Fatalf("root load after edu store: ok=%v entry=%+v", ok, entry)
	}

	// release on one form tombstones both
	registerOTPPending(root)
	releaseOTPPending(edu)
	if StorePushedOTP(root, "LATE", "") {
		t.Fatal("root store after edu release must be tombstoned")
	}
	if StorePushedOTP(edu, "LATE2", "") {
		t.Fatal("edu store after release must be tombstoned")
	}
}

func TestStorePushedOTP_WithoutPending(t *testing.T) {
	email := "race@example.com"
	clearOTPStateForTest(email)
	t.Cleanup(func() { clearOTPStateForTest(email) })
	if !StorePushedOTP(email, "ABC-DEF", "body") {
		t.Fatal("store without pending must succeed")
	}
	val, ok := otpCache.Load(email)
	if !ok {
		t.Fatal("not cached")
	}
	entry := val.(OTPEntry)
	if entry.Code != "ABC-DEF" {
		t.Fatalf("code=%q", entry.Code)
	}
	// empty rejected
	if StorePushedOTP(email, "", "") {
		t.Fatal("empty should fail")
	}
	// pending path still works
	registerOTPPending(email)
	if !StorePushedOTP(email, "XYZ-123", "") {
		t.Fatal("with pending")
	}
	releaseOTPPending(email)
}

func TestStorePushedOTPCompletedExpiryAndAddressReuse(t *testing.T) {
	email := "reused@example.com"
	clearOTPStateForTest(email)
	t.Cleanup(func() { clearOTPStateForTest(email) })

	registerOTPPending(email)
	releaseOTPPending(email)
	if StorePushedOTP(email, "111111", "") {
		t.Fatal("completed mailbox accepted callback")
	}

	registerOTPPending(email)
	if !StorePushedOTP(email, "222222", "") {
		t.Fatal("new mailbox lifecycle did not clear completed tombstone")
	}
	releaseOTPPending(email)

	otpLifecycleMu.Lock()
	expired := time.Now().Add(-time.Second)
	for _, k := range otpEmailAliasKeys(email) {
		otpCompleted[k] = expired
	}
	otpLifecycleMu.Unlock()
	if !StorePushedOTP(email, "333333", "") {
		t.Fatal("expired completed tombstone rejected early callback")
	}
}

func TestPruneOTPCompletedExpiresAndCapsOldestExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	completed := map[string]time.Time{
		"expired@example.com": now.Add(-time.Second),
		"oldest@example.com":  now.Add(time.Minute),
		"middle@example.com":  now.Add(2 * time.Minute),
		"latest@example.com":  now.Add(3 * time.Minute),
	}
	pruneOTPCompleted(completed, now, 2)
	if len(completed) != 2 {
		t.Fatalf("completed entries=%d, want 2", len(completed))
	}
	if _, ok := completed["expired@example.com"]; ok {
		t.Fatal("expired tombstone was not pruned")
	}
	if _, ok := completed["oldest@example.com"]; ok {
		t.Fatal("oldest expiry was not evicted at capacity")
	}
	if _, ok := completed["middle@example.com"]; !ok {
		t.Fatal("middle expiry was unexpectedly evicted")
	}
	if _, ok := completed["latest@example.com"]; !ok {
		t.Fatal("latest expiry was unexpectedly evicted")
	}
}

func TestStorePushedOTPConcurrentWithMailboxCompletion(t *testing.T) {
	email := "concurrent@example.com"
	clearOTPStateForTest(email)
	t.Cleanup(func() { clearOTPStateForTest(email) })
	registerOTPPending(email)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			StorePushedOTP(email, "444444", "")
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		releaseOTPPending(email)
	}()
	close(start)
	wg.Wait()

	// Establish completion after all racing callbacks, then verify the stable state.
	releaseOTPPending(email)
	if StorePushedOTP(email, "555555", "") {
		t.Fatal("completed mailbox accepted callback after concurrent lifecycle transition")
	}
}
