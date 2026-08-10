package grokregister

import "testing"

func headerVal(h OrderedHeaders, key string) string {
	return h.Get(key)
}

func hasHeader(h OrderedHeaders, key string) bool {
	return h.Has(key)
}

// RedirectNavHeaders must carry coherent navigation metadata + Client Hints for
// Chromium profiles, and compute Sec-Fetch-Site from the previous hop URL.
func TestRedirectNavHeadersChromiumCoherent(t *testing.T) {
	p := &BrowserProfile{
		Browser:  "chrome",
		OS:       "windows",
		UA:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/151.0.0.0 Safari/537.36",
		SecCHUA:  `"Chromium";v="151", "Not/A)Brand";v="24", "Google Chrome";v="151"`,
		Platform: "Windows",
	}

	// same-origin hop: accounts.x.ai → accounts.x.ai
	h := RedirectNavHeaders(p, "https://accounts.x.ai/account", "https://accounts.x.ai/sign-up", nil)
	if !hasHeader(h, "sec-ch-ua") {
		t.Fatal("chromium redirect must send sec-ch-ua")
	}
	if got := headerVal(h, "Sec-Fetch-Mode"); got != SecFetchModeNav {
		t.Fatalf("Sec-Fetch-Mode = %q, want %q", got, SecFetchModeNav)
	}
	if got := headerVal(h, "Sec-Fetch-Dest"); got != SecFetchDestDoc {
		t.Fatalf("Sec-Fetch-Dest = %q, want %q", got, SecFetchDestDoc)
	}
	if got := headerVal(h, "Sec-Fetch-Site"); got != SecFetchSiteSame {
		t.Fatalf("same-origin Sec-Fetch-Site = %q, want %q", got, SecFetchSiteSame)
	}
	// redirects are not user-activated
	if hasHeader(h, "Sec-Fetch-User") {
		t.Fatal("redirect nav must NOT send Sec-Fetch-User")
	}
	if got := headerVal(h, "User-Agent"); got != p.UA {
		t.Fatalf("User-Agent = %q, want profile UA", got)
	}

	// cross-site hop: accounts.x.ai → grok.com
	hx := RedirectNavHeaders(p, "https://grok.com/", "https://accounts.x.ai/account", nil)
	if got := headerVal(hx, "Sec-Fetch-Site"); got != SecFetchSiteCross {
		t.Fatalf("cross-site Sec-Fetch-Site = %q, want %q", got, SecFetchSiteCross)
	}
}

// Firefox/Safari profiles must NOT emit Chromium-only Client Hints.
func TestRedirectNavHeadersNonChromiumNoClientHints(t *testing.T) {
	p := &BrowserProfile{
		Browser: "firefox",
		OS:      "linux",
		UA:      "Mozilla/5.0 (X11; Linux x86_64; rv:155.0) Gecko/20100101 Firefox/155.0",
	}
	h := RedirectNavHeaders(p, "https://accounts.x.ai/account", "https://accounts.x.ai/sign-up", nil)
	if hasHeader(h, "sec-ch-ua") || hasHeader(h, "sec-ch-ua-platform") {
		t.Fatal("firefox redirect must NOT send sec-ch-ua headers")
	}
	if got := headerVal(h, "Sec-Fetch-Dest"); got != SecFetchDestDoc {
		t.Fatalf("Sec-Fetch-Dest = %q, want %q", got, SecFetchDestDoc)
	}
}

// First hop without a known previous URL falls back to Sec-Fetch-Site: none.
func TestRedirectNavHeadersEmptyPrevIsNone(t *testing.T) {
	p := &BrowserProfile{Browser: "chrome", UA: "ua", SecCHUA: "x", Platform: "Windows"}
	h := RedirectNavHeaders(p, "https://accounts.x.ai/account", "", nil)
	if got := headerVal(h, "Sec-Fetch-Site"); got != SecFetchSiteNone {
		t.Fatalf("empty prev Sec-Fetch-Site = %q, want %q", got, SecFetchSiteNone)
	}
}
