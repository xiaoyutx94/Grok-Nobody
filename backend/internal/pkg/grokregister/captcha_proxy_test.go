package grokregister

import "testing"

func TestNormalizeCaptchaProxyModeMigratesLegacyBool(t *testing.T) {
	if got := NormalizeCaptchaProxyMode("", true); got != CaptchaProxyModeRegistration {
		t.Fatalf("legacy true mode=%q", got)
	}
	if got := NormalizeCaptchaProxyMode("", false); got != CaptchaProxyModeDirect {
		t.Fatalf("legacy false mode=%q", got)
	}
	if got := NormalizeCaptchaProxyMode("global", false); got != CaptchaProxyModeGlobal {
		t.Fatalf("explicit mode=%q", got)
	}
}

func TestCaptchaProxyForWorkerKeepsRegistrationAndCaptchaRoutingIndependent(t *testing.T) {
	pool := []string{"socks5://global-a:1080", "socks5://global-b:1080"}
	registration := "socks5://registration:1080"
	cases := []struct {
		name     string
		mode     string
		workerID int
		want     string
	}{
		{name: "direct", mode: CaptchaProxyModeDirect, want: ""},
		{name: "registration", mode: CaptchaProxyModeRegistration, want: registration},
		{name: "global first", mode: CaptchaProxyModeGlobal, want: pool[0]},
		{name: "global rotates", mode: CaptchaProxyModeGlobal, workerID: 1, want: pool[1]},
		{name: "selected", mode: CaptchaProxyModeSelected, workerID: 9, want: pool[0]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := captchaProxyForWorker(tc.mode, pool, registration, tc.workerID); got != tc.want {
				t.Fatalf("proxy=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestCaptchaProxyExplicitDirectDoesNotInheritRegistrationProxy(t *testing.T) {
	registration := "socks5://registration:1080"
	if got := captchaProxyWithLegacyFallback(CaptchaOptions{}, registration); got != registration {
		t.Fatalf("legacy fallback=%q", got)
	}
	if got := captchaProxyWithLegacyFallback(CaptchaOptions{ProxyExplicit: true}, registration); got != "" {
		t.Fatalf("explicit direct inherited proxy=%q", got)
	}
	selected := "http://captcha-only:8080"
	if got := captchaProxyWithLegacyFallback(CaptchaOptions{ProxyURL: selected, ProxyExplicit: true}, registration); got != selected {
		t.Fatalf("selected proxy=%q", got)
	}
}
