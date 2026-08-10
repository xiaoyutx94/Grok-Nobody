package grokregister

import "strings"

const (
	CaptchaProxyModeDirect       = "direct"
	CaptchaProxyModeRegistration = "registration"
	CaptchaProxyModeGlobal       = "global"
	CaptchaProxyModeSelected     = "selected"
)

// ParseCaptchaProxyMode accepts canonical values plus legacy-friendly aliases.
func ParseCaptchaProxyMode(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case CaptchaProxyModeDirect, "off", "none", "server":
		return CaptchaProxyModeDirect, true
	case CaptchaProxyModeRegistration, "register", "follow", "same":
		return CaptchaProxyModeRegistration, true
	case CaptchaProxyModeGlobal, "global_pool", "global-proxy":
		return CaptchaProxyModeGlobal, true
	case CaptchaProxyModeSelected, "single", "fixed":
		return CaptchaProxyModeSelected, true
	default:
		return "", false
	}
}

// NormalizeCaptchaProxyMode migrates the old solve-via-proxy bool when no valid
// explicit mode is available.
func NormalizeCaptchaProxyMode(raw string, legacySolveViaProxy bool) string {
	if mode, ok := ParseCaptchaProxyMode(raw); ok {
		return mode
	}
	if legacySolveViaProxy {
		return CaptchaProxyModeRegistration
	}
	return CaptchaProxyModeDirect
}

func CaptchaProxyModeUsesProxy(mode string) bool {
	return NormalizeCaptchaProxyMode(mode, false) != CaptchaProxyModeDirect
}

func captchaProxyWithLegacyFallback(opts CaptchaOptions, registrationProxy string) string {
	if opts.ProxyURL == "" && !opts.ProxyExplicit {
		return registrationProxy
	}
	return opts.ProxyURL
}
