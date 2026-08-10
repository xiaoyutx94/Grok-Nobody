package grokregister

import (
	"fmt"
	"testing"
)

func TestGrokSubmitNeedsFreshCaptcha(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{200, `server action failed: invalid turnstile token`, true},
		{200, `server action returned no redirect`, false},
		{403, `{"error":"invalid turnstile token"}`, true},
		{422, `captcha challenge failed`, true},
		{429, `turnstile`, false},
		{500, `captcha`, false},
		{403, `proxy access denied`, false},
	} {
		if got := grokSubmitNeedsFreshCaptcha(tc.status, tc.body); got != tc.want {
			t.Fatalf("status=%d body=%q got=%v want=%v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestCaptchaSolveAttemptsRetriesOnlyRecoverableEzSolverErrors(t *testing.T) {
	if got := captchaSolveAttempts(CaptchaProviderEzSolver, fmt.Errorf("hard-timeout 35s (CDP/page hung)")); got != 2 {
		t.Fatalf("recoverable attempts=%d want=2", got)
	}
	if got := captchaSolveAttempts(CaptchaProviderEzSolver, fmt.Errorf("HTTP 401 unauthorized")); got != 1 {
		t.Fatalf("permanent attempts=%d want=1", got)
	}
	if got := captchaSolveAttempts(CaptchaProviderYesCaptcha, fmt.Errorf("hard-timeout")); got != 1 {
		t.Fatalf("paid provider attempts=%d want=1", got)
	}
}

func TestAuralithRetrySettingAddsToInitialSolve(t *testing.T) {
	previous := GetCaptchaRuntime()
	t.Cleanup(func() { SetCaptchaRuntime(previous) })

	SetCaptchaRuntime(CaptchaRuntime{AuralithRetry: 0})
	if got := captchaSolveAttemptLimitFor(CaptchaProviderAuralith); got != 1 {
		t.Fatalf("retry=0 attempt limit=%d want=1", got)
	}
	SetCaptchaRuntime(CaptchaRuntime{AuralithRetry: 1})
	if got := captchaSolveAttemptLimitFor(CaptchaProviderAuralith); got != 2 {
		t.Fatalf("retry=1 attempt limit=%d want=2", got)
	}
	if got := captchaSolveAttempts(CaptchaProviderAuralith, fmt.Errorf("soft-timeout")); got != 2 {
		t.Fatalf("Auralith recoverable attempts=%d want=2", got)
	}
}
