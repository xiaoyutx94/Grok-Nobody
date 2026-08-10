package grokregister

import (
	"testing"
	"time"
)

// TestStopEzTokenWarmupTerminatesLoop verifies the warmup filler can be fully
// stopped — the fix for "停止注册后 EzSolver 仍在后台打码/占内存". Before, the loop
// was an immortal for{} that Resume re-enabled after every batch.
func TestStopEzTokenWarmupTerminatesLoop(t *testing.T) {
	// Clean slate (shared package globals).
	StopEzTokenWarmup("test-setup")

	// Point at a refused port so warm fills fail fast without real Chrome.
	cfg := EzSolverConfig{
		URL:            "http://127.0.0.1:9",
		TimeoutMS:      2000,
		MaxConcurrency: 2,
		Enabled:        true,
	}
	ConfigureEzTokenWarmup(cfg, CaptchaOptions{EzSolverURL: cfg.URL},
		"https://accounts.x.ai/sign-up", "0xTESTSITEKEY", 2)

	ezPoolMu.Lock()
	running := ezPoolRunning
	hasStop := ezPoolStopCh != nil
	ezPoolMu.Unlock()
	if !running || !hasStop {
		t.Fatalf("warmup should be running after Configure: running=%v hasStop=%v", running, hasStop)
	}

	StopEzTokenWarmup("test-stop")

	ezPoolMu.Lock()
	running = ezPoolRunning
	stopCh := ezPoolStopCh
	target := ezPoolTarget
	poolLen := len(ezPool)
	ezPoolMu.Unlock()
	if running {
		t.Fatal("ezPoolRunning must be false after StopEzTokenWarmup")
	}
	if stopCh != nil {
		t.Fatal("ezPoolStopCh must be nil after StopEzTokenWarmup")
	}
	if target != 0 {
		t.Fatalf("ezPoolTarget must reset to 0, got %d", target)
	}
	if poolLen != 0 {
		t.Fatalf("pool must be cleared on stop, got %d tokens", poolLen)
	}

	// Idempotent: a second stop must not panic (double-close guard).
	StopEzTokenWarmup("test-stop-again")

	// A fresh Configure must be able to start a new loop after a stop.
	ConfigureEzTokenWarmup(cfg, CaptchaOptions{EzSolverURL: cfg.URL},
		"https://accounts.x.ai/sign-up", "0xTESTSITEKEY", 2)
	ezPoolMu.Lock()
	restarted := ezPoolRunning
	ezPoolMu.Unlock()
	if !restarted {
		t.Fatal("Configure after Stop must be able to restart the warmup loop")
	}
	StopEzTokenWarmup("test-teardown")
	// Give the goroutine a moment to observe the closed channel and exit.
	time.Sleep(50 * time.Millisecond)
}
