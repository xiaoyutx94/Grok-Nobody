package grokregister

import (
	"os"
	"strings"
	"testing"
)

// TestGrokInitConfigThroughProxy is a live, opt-in probe that runs the REAL
// grokInitConfig (sign-up page GET + JS chunk scan) through a residential proxy
// and asserts x.ai still serves the Next.js app and an ActionID is extractable.
// It exists to verify header changes to grokInitConfig do not get the page
// Cloudflare-blocked. Skipped unless GROK_PROBE=1.
//
//	GROK_PROBE=1 GROK_PROBE_PROXY='socks5h://u:p@host:port' \
//	  go test ./internal/pkg/grokregister/ -run TestGrokInitConfigThroughProxy -v -count=1 -timeout 3m
func TestGrokInitConfigThroughProxy(t *testing.T) {
	if os.Getenv("GROK_PROBE") != "1" {
		t.Skip("set GROK_PROBE=1 to run the live grokInitConfig probe")
	}
	proxy := strings.TrimSpace(os.Getenv("GROK_PROBE_PROXY"))
	profile := PickBrowserProfile("chrome", "windows")
	client := NewHTTPClientWithBrowser(proxy, proxy != "", "chrome")
	defer client.Close()

	cfg, err := grokInitConfig(client, &profile)
	if err != nil {
		t.Fatalf("grokInitConfig failed (page blocked or no ActionID?): %v", err)
	}
	if strings.TrimSpace(cfg.ActionID) == "" {
		t.Fatal("ActionID empty — x.ai served the page but action extraction failed")
	}
	t.Logf("OK actionID=%s sitekey=%s (headers accepted by x.ai via %s)", cfg.ActionID, cfg.SiteKey, MaskProxy(proxy))
}

// TestGrokGRPCHeadersAcceptedLive verifies the coherent grpcHeaders (now carrying
// sec-ch-ua + Fetch Metadata) are accepted by x.ai's gRPC-Web send-code endpoint.
// It runs the real init + Mail.tm email + CreateEmailValidationCode; a 200 with no
// gRPC trailer error proves the new header set is accepted (no account is created —
// this only requests an email code). Skipped unless GROK_PROBE=1.
func TestGrokGRPCHeadersAcceptedLive(t *testing.T) {
	if os.Getenv("GROK_PROBE") != "1" {
		t.Skip("set GROK_PROBE=1 to run the live gRPC header probe")
	}
	proxy := strings.TrimSpace(os.Getenv("GROK_PROBE_PROXY"))
	profile := PickBrowserProfile("chrome", "windows")
	reg := NewGrokRegistrar(proxy, NewSafeCancel(), &profile)
	defer func() {
		if reg.client != nil {
			reg.client.Close()
		}
	}()

	if _, err := grokInitConfig(reg.client, &profile); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	mail := NewHTTPClientWithBrowser("", false, "")
	defer mail.Close()
	acc, err := mailtmCreateAccount(mail)
	if err != nil {
		t.Skipf("mail.tm unavailable, cannot probe gRPC: %v", err)
	}
	if err := reg.sendEmailCode(acc.Email); err != nil {
		t.Fatalf("sendEmailCode rejected with coherent gRPC headers: %v", err)
	}
	t.Logf("OK x.ai accepted CreateEmailValidationCode for %s with coherent gRPC headers", acc.Email)
}
