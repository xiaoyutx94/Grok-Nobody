package grokregister

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizeCaptchaProviderVeloraTurn(t *testing.T) {
	if got := normalizeCaptchaProvider("veloraturn"); got != CaptchaProviderVeloraTurn {
		t.Fatalf("provider=%q want=%q", got, CaptchaProviderVeloraTurn)
	}
}

func TestPostCompatibleSolverPreservesVeloraTurnProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"123456789012345678901","elapsed":0.25}`))
	}))
	defer server.Close()

	result, err := postEzSolverOnce(
		server.URL,
		"",
		"https://example.test",
		"site-key",
		10,
		5*time.Second,
		nil,
		"",
		CaptchaProviderVeloraTurn,
	)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if result.Provider != CaptchaProviderVeloraTurn {
		t.Fatalf("provider=%q want=%q", result.Provider, CaptchaProviderVeloraTurn)
	}
}
