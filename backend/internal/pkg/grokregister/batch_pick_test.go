package grokregister

import (
	"sync/atomic"
	"testing"
)

func TestRunBatch_CFPickModes(t *testing.T) {
	// Build 3 dummy CF workers (won't actually register — cancel immediately)
	svcs := []EmailService{
		NewCFWorkerEmail("a.com", "https://a.example", "k", "", "", ""),
		NewCFWorkerEmail("b.com", "https://b.example", "k", "", "", ""),
		NewCFWorkerEmail("c.com", "https://c.example", "k", "", "", ""),
	}
	for _, mode := range []string{"round_robin", "random"} {
		cancel := NewSafeCancel()
		cancel.Close() // cancel before workers start meaningful work
		var hits int32
		prog := RunBatch(BatchOptions{
			Count:         3,
			Concurrency:   2,
			EmailMode:     "cfworker",
			EmailServices: svcs,
			CFPickMode:    mode,
			Captcha:       CaptchaOptions{Provider: CaptchaProviderNone},
			OnResult: func(r RegisterResult) {
				atomic.AddInt32(&hits, 1)
			},
		}, cancel)
		if prog.Running {
			t.Fatalf("mode=%s still running", mode)
		}
		// cancelled jobs may complete 0 if workers exit before jobs; either way no panic
		t.Logf("mode=%s completed=%d hits=%d", mode, prog.Completed, hits)
	}
}
