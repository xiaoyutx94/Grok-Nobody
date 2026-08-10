package grokregister

import (
	"strings"
	"testing"
)

func TestSafeGrokURLForLogDropsCredentialsQueryAndFragment(t *testing.T) {
	got := safeGrokURLForLog("https://user:pass@accounts.x.ai/set-cookie/path?token=secret#frag")
	if got != "https://accounts.x.ai/set-cookie/path" {
		t.Fatalf("safe URL=%q", got)
	}
	for _, secret := range []string{"user", "pass", "token", "secret", "frag"} {
		if strings.Contains(got, secret) {
			t.Fatalf("safe URL leaked %q: %s", secret, got)
		}
	}
}

func TestSafeGrokURLForLogRedactsLongPathSegments(t *testing.T) {
	secret := strings.Repeat("a", 80)
	got := safeGrokURLForLog("https://accounts.x.ai/callback/" + secret + "?token=hidden")
	if strings.Contains(got, secret) || strings.Contains(got, "hidden") {
		t.Fatalf("safe URL leaked a path/query token: %s", got)
	}
}

func TestSafeGrokURLForLogRejectsRelativeOrMalformed(t *testing.T) {
	for _, raw := range []string{"/relative?token=secret", "not a url", ""} {
		if got := safeGrokURLForLog(raw); got != "<redacted-url>" {
			t.Fatalf("safe URL(%q)=%q", raw, got)
		}
	}
}

func TestSanitizeGrokLogTextRemovesURLSecrets(t *testing.T) {
	got := sanitizeGrokLogText(`prefix https://accounts.x.ai/set-cookie?token=secret suffix`)
	if strings.Contains(got, "secret") || strings.Contains(got, "token=") {
		t.Fatalf("sanitized text leaked secret: %s", got)
	}
	if !strings.Contains(got, "https://accounts.x.ai/set-cookie") {
		t.Fatalf("sanitized text lost useful path: %s", got)
	}
}
