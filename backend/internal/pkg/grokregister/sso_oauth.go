package grokregister

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Mirror Wei-Shaw/sub2api backend/internal/pkg/xai/sso_device.go:
// free Grok web SSO cookies are converted to Build OAuth tokens via device flow.

const (
	grokSSOBuildScope   = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"
	grokSSOMinimalScope = "openid profile email offline_access grok-cli:access api:access"
	grokSSOAccountsURL  = "https://accounts.x.ai/"
	grokOAuthIssuer     = "https://auth.x.ai"
	// Official confirm APIs (no browser): POST /oauth2/device/verify then /oauth2/device/approve.
	grokSSODeviceURL      = grokOAuthIssuer + "/oauth2/device/code"
	grokSSOVerifyURL      = grokOAuthIssuer + "/oauth2/device/verify"
	grokSSOApproveURL     = grokOAuthIssuer + "/oauth2/device/approve"
	grokSSOTokenURL       = grokOAuthIssuer + "/oauth2/token"
	grokDefaultClientID   = "b1a00492-073a-47ea-816f-4c329264a828"
	grokCLIChatBaseURL    = "https://cli-chat-proxy.grok.com/v1"
	grokDefaultCLIVersion = "0.2.93"

	// SSOConversionTimeout bounds both fresh device-flow attempts. The stale-grant
	// retry normally happens immediately after the first token poll.
	SSOConversionTimeout       = 5 * time.Minute
	grokSSOVerificationTimeout = SSOConversionTimeout + 15*time.Second
	grokSSOGrantMaxAttempts    = 2
	grokSSOGrantRetryDelay     = 2 * time.Second
	grokSSOPropagationDelay    = 2 * time.Second
)

var (
	ErrGrokSSOAuthorizationDenied = errors.New("xAI device authorization denied")
	errGrokSSOUnauthorized        = errors.New("grok sso unauthorized")
	errGrokSSODeviceGrantStale    = errors.New("xAI device authorization state is stale")
)

// GrokOAuthToken is the result of SSO → device OAuth conversion.
type GrokOAuthToken = grokOAuthToken

// grokOAuthToken is the result of SSO → device OAuth conversion.
type grokOAuthToken struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	ExpiresIn    int64
	Scope        string
	Email        string
	Subject      string
}

// normalizeGrokSSOToken accepts raw JWT, "sso=...", "sso-rw=...", or full Cookie header.
func normalizeGrokSSOToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "cookie:") {
		value = strings.TrimSpace(value[len("cookie:"):])
	}
	for _, part := range strings.Split(value, ";") {
		name, token, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "sso", "sso-rw":
			return sanitizeGrokSSOToken(token)
		}
	}
	if token, _, found := strings.Cut(value, ";"); found {
		value = strings.TrimSpace(token)
	}
	return sanitizeGrokSSOToken(value)
}

func sanitizeGrokSSOToken(value string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(strings.TrimSpace(value))
}

func decodeGrokJWTClaims(token string) map[string]interface{} {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]interface{}
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func jwtClaimString(claims map[string]interface{}, key string) string {
	if claims == nil {
		return ""
	}
	v, _ := claims[key].(string)
	return strings.TrimSpace(v)
}

// grokProbeSSOSession checks whether web SSO is accepted by accounts.x.ai
// (sub2api first step: GET accounts.x.ai must not bounce to sign-in/sign-up).
func grokProbeSSOSession(ctx context.Context, sso, ssoRw, proxy string, proxyOn bool) error {
	sso = normalizeGrokSSOToken(sso)
	if sso == "" {
		return fmt.Errorf("empty sso")
	}
	if ssoRw == "" {
		ssoRw = sso
	} else {
		ssoRw = normalizeGrokSSOToken(ssoRw)
		if ssoRw == "" {
			ssoRw = sso
		}
	}

	client := NewHTTPClientWithBrowser(proxy, proxyOn, "chrome")
	defer client.Close()
	stopClose := context.AfterFunc(ctx, client.Close)
	defer stopClose()
	client.SetCookie("sso", sso)
	client.SetCookie("sso-rw", ssoRw)

	headers := OrderedHeaders{
		{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"},
		{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		{"Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8"},
		{"Cookie", "sso=" + sso + "; sso-rw=" + ssoRw},
	}

	current := grokSSOAccountsURL
	var lastStatus int
	var lastURL string
	for hop := 0; hop < 8; hop++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.GetOrdered(current, headers)
		if err != nil {
			return fmt.Errorf("accounts.x.ai probe: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		client.SaveCookies(resp)
		lastStatus = resp.StatusCode
		lastURL = current
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Location()
			if loc == "" {
				break
			}
			base, _ := url.Parse(current)
			next, err := url.Parse(loc)
			if err != nil {
				return fmt.Errorf("bad redirect: %w", err)
			}
			current = base.ResolveReference(next).String()
			headers.Set("Cookie", "sso="+sso+"; sso-rw="+ssoRw)
			continue
		}
		break
	}

	lower := strings.ToLower(lastURL + " " + current)
	if lastStatus == 401 || strings.Contains(lower, "sign-in") || strings.Contains(lower, "sign-up") {
		return fmt.Errorf("%w (status=%d url=%s)", errGrokSSOUnauthorized, lastStatus, current)
	}
	if lastStatus < 200 || lastStatus >= 400 {
		return fmt.Errorf("accounts.x.ai HTTP %d", lastStatus)
	}
	return nil
}

type grokSSODeviceFlow struct {
	client    grokSSODeviceHTTPClient
	userAgent string
	jar       http.CookieJar
	sleep     func(context.Context, time.Duration) error
}

type grokSSODeviceHTTPClient interface {
	GetOrdered(string, OrderedHeaders) (*Response, error)
	PostRawOrdered(string, OrderedHeaders, []byte) (*Response, error)
}

type grokSSOConvertOptions struct {
	newClient func() (grokSSODeviceHTTPClient, func())
	sleep     func(context.Context, time.Duration) error
}

// ConvertSSOToOAuth converts web SSO cookie to Build OAuth tokens via Chrome TLS fingerprint (azuretls).
func ConvertSSOToOAuth(ctx context.Context, sso, ssoRw, proxy string, proxyOn bool) (*grokOAuthToken, error) {
	return convertGrokSSOToOAuth(ctx, sso, ssoRw, proxy, proxyOn)
}

// convertGrokSSOToOAuth converts web SSO cookie to Build OAuth tokens (sub2api ConvertSSOToBuild).
func convertGrokSSOToOAuth(ctx context.Context, sso, ssoRw, proxy string, proxyOn bool) (*grokOAuthToken, error) {
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			client := NewHTTPClientWithBrowser(proxy, proxyOn, "chrome")
			stopClose := context.AfterFunc(ctx, client.Close)
			return client, func() {
				stopClose()
				client.Close()
			}
		},
		sleep: grokSleepContext,
	}
	return convertGrokSSOToOAuthWithOptions(ctx, sso, ssoRw, opts)
}

func convertGrokSSOToOAuthWithOptions(ctx context.Context, sso, ssoRw string, opts grokSSOConvertOptions) (*grokOAuthToken, error) {
	sso = normalizeGrokSSOToken(sso)
	if sso == "" {
		return nil, fmt.Errorf("empty sso")
	}
	if ssoRw == "" {
		ssoRw = sso
	} else {
		ssoRw = normalizeGrokSSOToken(ssoRw)
		if ssoRw == "" {
			ssoRw = sso
		}
	}

	if opts.newClient == nil {
		return nil, errors.New("create xAI device client: missing factory")
	}
	if opts.sleep == nil {
		opts.sleep = grokSleepContext
	}
	var staleErr error
	for attempt := 1; attempt <= grokSSOGrantMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		client, closeClient := opts.newClient()
		if client == nil {
			return nil, errors.New("create xAI device client: nil client")
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			if closeClient != nil {
				closeClient()
			}
			return nil, fmt.Errorf("create xAI cookie jar: %w", err)
		}
		flow := &grokSSODeviceFlow{
			client:    client,
			userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			jar:       jar,
			sleep:     opts.sleep,
		}
		flow.seedSSOCookies(sso, ssoRw)
		scope, profile := grokSSOScopeForAttempt(attempt)
		Logf("[Grok OAuth] device flow scope profile=%s", profile)
		token, err := flow.convertAttempt(ctx, scope)
		if closeClient != nil {
			closeClient()
		}
		if err == nil {
			return token, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !errors.Is(err, errGrokSSODeviceGrantStale) {
			return nil, err
		}
		staleErr = err
		if attempt == grokSSOGrantMaxAttempts {
			break
		}
		if err := opts.sleep(ctx, grokSSOGrantRetryDelay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w after %d fresh device flows: %v", ErrGrokSSOAuthorizationDenied, grokSSOGrantMaxAttempts, staleErr)
}

func grokSSOScopeForAttempt(attempt int) (scope, profile string) {
	if attempt <= 1 {
		return grokSSOBuildScope, "full"
	}
	return grokSSOMinimalScope, "minimal"
}

func (flow *grokSSODeviceFlow) convertAttempt(ctx context.Context, scope string) (*grokOAuthToken, error) {
	status, finalURL, _, err := flow.do(ctx, "GET", grokSSOAccountsURL, nil)
	if err != nil {
		return nil, err
	}
	if status == 401 || strings.Contains(finalURL, "sign-in") || strings.Contains(finalURL, "sign-up") {
		return nil, errGrokSSOUnauthorized
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("validate sso: HTTP %d", status)
	}
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	for attempt := 1; attempt <= 6; attempt++ {
		status, _, body, requestErr := flow.do(ctx, "POST", grokSSODeviceURL, url.Values{
			"client_id": {grokDefaultClientID},
			"scope":     {scope},
		})
		if requestErr != nil {
			return nil, requestErr
		}
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			if attempt == 6 {
				return nil, fmt.Errorf("device code: HTTP %d", status)
			}
			if err := flow.sleep(ctx, grokSSOStartBackoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("device code: HTTP %d %s", status, truncStr(string(body), 180))
		}
		if err := json.Unmarshal(body, &device); err != nil {
			return nil, fmt.Errorf("parse device response: %w", err)
		}
		break
	}
	if device.DeviceCode == "" || device.UserCode == "" || !safeGrokAuthURL(device.VerificationURIComplete) {
		return nil, fmt.Errorf("incomplete device flow response")
	}
	if device.Interval <= 0 {
		device.Interval = 5
	}
	if device.ExpiresIn <= 0 {
		device.ExpiresIn = 1800
	}

	// 2026-07 official UI: "Sign in to Grok Build" / Continue then consent Allow.
	status, verificationURL, verificationBody, err := flow.do(ctx, "GET", device.VerificationURIComplete, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("open device verification: HTTP %d", status)
	}

	verifyAction, verifyMethod, verifyForm, formErr := selectGrokHTMLForm(verificationBody, verificationURL, "verify")
	if formErr != nil {
		verifyAction, verifyMethod = grokSSOVerifyURL, http.MethodPost
		verifyForm = url.Values{"user_code": {device.UserCode}}
	} else {
		verifyForm.Set("user_code", device.UserCode)
	}
	status, finalURL, consentBody, err := flow.submitHTMLForm(ctx, verifyMethod, verifyAction, verificationURL, verifyForm)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("verify device code (Continue): HTTP %d", status)
	}
	approveAction, approveMethod, approveForm, formErr := selectGrokHTMLForm(consentBody, finalURL, "approve")
	if formErr != nil {
		if !strings.Contains(strings.ToLower(finalURL+" "+string(consentBody)), "consent") {
			return nil, fmt.Errorf("device verification did not reach consent page")
		}
		approveAction, approveMethod = grokSSOApproveURL, http.MethodPost
		approveForm = url.Values{
			"user_code":      {device.UserCode},
			"action":         {"allow"},
			"principal_type": {"User"},
		}
	} else {
		approveForm.Set("user_code", device.UserCode)
		if approveForm.Get("action") == "" {
			approveForm.Set("action", "allow")
		}
	}
	status, finalURL, approveBody, err := flow.submitHTMLForm(ctx, approveMethod, approveAction, finalURL, approveForm)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("approve device code: HTTP %d", status)
	}
	if !looksLikeGrokDeviceDone(finalURL, approveBody) {
		return nil, fmt.Errorf("device approval did not reach an authorized page")
	}

	return flow.pollToken(ctx, device.DeviceCode, time.Duration(device.Interval)*time.Second, time.Duration(device.ExpiresIn)*time.Second)
}

func (f *grokSSODeviceFlow) pollToken(ctx context.Context, deviceCode string, interval, expiresIn time.Duration) (*grokOAuthToken, error) {
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(expiresIn)
	if max := time.Now().Add(150 * time.Second); deadline.After(max) {
		deadline = max
	}
	for time.Now().Before(deadline) {
		if err := f.sleep(ctx, interval); err != nil {
			return nil, err
		}
		status, _, body, err := f.do(ctx, "POST", grokSSOTokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {grokDefaultClientID},
			"device_code": {deviceCode},
		})
		if err != nil {
			return nil, err
		}
		var payload struct {
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			IDToken          string `json:"id_token"`
			TokenType        string `json:"token_type"`
			ExpiresIn        int64  `json:"expires_in"`
			Scope            string `json:"scope"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("parse token: %w", err)
		}
		if status >= 200 && status < 300 && payload.AccessToken != "" && payload.RefreshToken != "" {
			if payload.ExpiresIn <= 0 {
				payload.ExpiresIn = int64((6 * time.Hour).Seconds())
			}
			if payload.TokenType == "" {
				payload.TokenType = "Bearer"
			}
			tok := &grokOAuthToken{
				AccessToken:  payload.AccessToken,
				RefreshToken: payload.RefreshToken,
				IDToken:      payload.IDToken,
				TokenType:    payload.TokenType,
				ExpiresIn:    payload.ExpiresIn,
				Scope:        payload.Scope,
			}
			claims := decodeGrokJWTClaims(payload.IDToken)
			if claims == nil {
				claims = decodeGrokJWTClaims(payload.AccessToken)
			}
			tok.Email = jwtClaimString(claims, "email")
			tok.Subject = jwtClaimString(claims, "sub")
			return tok, nil
		}
		if status >= 200 && status < 300 && payload.AccessToken != "" {
			return nil, fmt.Errorf("device authorization returned no refresh token")
		}
		switch payload.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "invalid_grant":
			desc := firstNonEmptyStr(payload.ErrorDescription, payload.Error)
			if isGrokSSORetryableStaleGrant(payload.Error, payload.ErrorDescription) {
				return nil, fmt.Errorf("%w: %s", errGrokSSODeviceGrantStale, desc)
			}
			return nil, fmt.Errorf("%w: confirm ok (verify+approve) but token grant denied: %s", ErrGrokSSOAuthorizationDenied, desc)
		case "access_denied", "expired_token":
			// verify+approve already done; token-side grant rejection (common on new free).
			// Official: {"error":"invalid_grant","error_description":"Access denied"}
			desc := firstNonEmptyStr(payload.ErrorDescription, payload.Error)
			return nil, fmt.Errorf("%w: confirm ok (verify+approve) but token grant denied: %s", ErrGrokSSOAuthorizationDenied, desc)
		default:
			if status >= 400 {
				return nil, fmt.Errorf("token poll HTTP %d: %s", status, firstNonEmptyStr(payload.ErrorDescription, payload.Error))
			}
			return nil, fmt.Errorf("token poll failed: %s", firstNonEmptyStr(payload.ErrorDescription, payload.Error, fmt.Sprintf("%d", status)))
		}
	}
	return nil, fmt.Errorf("device flow token poll timed out")
}

func (f *grokSSODeviceFlow) do(ctx context.Context, method, endpoint string, form url.Values) (int, string, []byte, error) {
	return f.doWithReferer(ctx, method, endpoint, form, "")
}

func (f *grokSSODeviceFlow) submitHTMLForm(ctx context.Context, method, endpoint, referer string, form url.Values) (int, string, []byte, error) {
	if strings.EqualFold(method, http.MethodGet) {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return 0, endpoint, nil, err
		}
		query := parsed.Query()
		for key, values := range form {
			query.Del(key)
			for _, value := range values {
				query.Add(key, value)
			}
		}
		parsed.RawQuery = query.Encode()
		return f.doWithReferer(ctx, http.MethodGet, parsed.String(), nil, referer)
	}
	return f.doWithReferer(ctx, http.MethodPost, endpoint, form, referer)
}

func (f *grokSSODeviceFlow) doWithReferer(ctx context.Context, method, endpoint string, form url.Values, referer string) (int, string, []byte, error) {
	if !safeGrokAuthURL(endpoint) {
		return 0, "", nil, fmt.Errorf("untrusted oauth url")
	}
	currentURL := endpoint
	currentMethod := method
	currentForm := form
	for redirects := 0; redirects <= 8; redirects++ {
		select {
		case <-ctx.Done():
			return 0, currentURL, nil, ctx.Err()
		default:
		}

		var body []byte
		headers := OrderedHeaders{
			{"Accept", "application/json, text/html;q=0.9, */*;q=0.8"},
			{"Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8"},
			{"User-Agent", f.userAgent},
			{"Cookie", f.cookieHeader(currentURL)},
		}
		if currentForm != nil {
			enc := currentForm.Encode()
			body = []byte(enc)
			headers = append(headers, []string{"Content-Type", "application/x-www-form-urlencoded"})
			if origin := grokXAIOrigin(referer); origin != "" {
				headers = append(headers, []string{"Origin", origin})
				headers = append(headers, []string{"Referer", referer})
			}
		}

		var resp *Response
		var err error
		switch strings.ToUpper(currentMethod) {
		case "GET":
			resp, err = f.client.GetOrdered(currentURL, headers)
		default:
			resp, err = f.client.PostRawOrdered(currentURL, headers, body)
		}
		if err != nil {
			return 0, currentURL, nil, err
		}
		if err := ctx.Err(); err != nil {
			return 0, currentURL, nil, err
		}
		f.captureCookies(currentURL, resp)

		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			return resp.StatusCode, currentURL, resp.Body, nil
		}
		loc := strings.TrimSpace(resp.Location())
		if loc == "" {
			return resp.StatusCode, currentURL, resp.Body, fmt.Errorf("oauth redirect missing Location")
		}
		base, _ := url.Parse(currentURL)
		next, err := url.Parse(loc)
		if err != nil {
			return resp.StatusCode, currentURL, resp.Body, err
		}
		currentURL = base.ResolveReference(next).String()
		if !safeGrokAuthURL(currentURL) {
			return resp.StatusCode, currentURL, resp.Body, fmt.Errorf("oauth redirected to untrusted host")
		}
		if resp.StatusCode == 303 || ((resp.StatusCode == 301 || resp.StatusCode == 302) && currentMethod != "GET" && currentMethod != "HEAD") {
			currentMethod = "GET"
			currentForm = nil
		}
	}
	return 0, currentURL, nil, fmt.Errorf("oauth redirected too many times")
}

func (f *grokSSODeviceFlow) seedSSOCookies(sso, ssoRw string) {
	base, _ := url.Parse(grokSSOAccountsURL)
	f.jar.SetCookies(base, []*http.Cookie{
		{Name: "sso", Value: sso, Path: "/", Domain: "x.ai", Secure: true},
		{Name: "sso-rw", Value: ssoRw, Path: "/", Domain: "x.ai", Secure: true},
	})
}

func (f *grokSSODeviceFlow) captureCookies(rawURL string, resp *Response) {
	parsed, err := url.Parse(rawURL)
	if err != nil || resp == nil {
		return
	}
	standard := (&http.Response{Header: resp.Headers}).Cookies()
	allowed := make([]*http.Cookie, 0, len(standard))
	for _, cookie := range standard {
		name := strings.TrimSpace(cookie.Name)
		value := strings.TrimSpace(cookie.Value)
		if !allowedGrokXAISessionCookie(name) || len(value) > 16384 || strings.ContainsAny(name+value, "\r\n\x00") {
			continue
		}
		allowed = append(allowed, cookie)
	}
	f.jar.SetCookies(parsed, allowed)
}

func (f *grokSSODeviceFlow) cookieHeader(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	cookies := f.jar.Cookies(parsed)
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func allowedGrokXAISessionCookie(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || len(lower) > 128 {
		return false
	}
	switch lower {
	case "sso", "sso-rw",
		"session", "xai_session", "oauth_session",
		"csrf", "csrf-token", "_csrf", "state", "oauth_state",
		"ory_kratos_session", "ory_kratos_continuity",
		"ory_hydra_login_csrf", "ory_hydra_consent_csrf":
		return true
	}
	return false
}

func isGrokSSORetryableStaleGrant(code, description string) bool {
	return strings.EqualFold(strings.TrimSpace(code), "invalid_grant") &&
		strings.Contains(strings.ToLower(strings.TrimSpace(description)), "access denied")
}

func grokXAIOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || !safeGrokAuthURL(rawURL) {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

type grokHTMLForm struct {
	action string
	method string
	fields url.Values
	score  int
}

func selectGrokHTMLForm(body []byte, pageURL, mode string) (string, string, url.Values, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", "", nil, fmt.Errorf("parse xAI OAuth HTML: %w", err)
	}
	base, err := url.Parse(pageURL)
	if err != nil || !safeGrokAuthURL(pageURL) {
		return "", "", nil, fmt.Errorf("untrusted oauth page url")
	}
	forms := make([]grokHTMLForm, 0, 2)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "form" {
			action := strings.TrimSpace(grokHTMLAttr(node, "action"))
			resolved, resolveErr := url.Parse(action)
			if resolveErr == nil {
				action = base.ResolveReference(resolved).String()
			}
			method := strings.ToUpper(strings.TrimSpace(grokHTMLAttr(node, "method")))
			if method == "" {
				method = http.MethodGet
			}
			fields := url.Values{}
			collectGrokFormFields(node, mode, fields)
			forms = append(forms, grokHTMLForm{action: action, method: method, fields: fields, score: scoreGrokForm(action, fields, mode)})
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	best := -1
	bestScore := -1
	for i := range forms {
		if forms[i].score > bestScore && safeGrokAuthURL(forms[i].action) &&
			(forms[i].method == http.MethodGet || forms[i].method == http.MethodPost) {
			best = i
			bestScore = forms[i].score
		}
	}
	if best < 0 || bestScore <= 0 {
		return "", "", nil, fmt.Errorf("xAI OAuth %s form not found", mode)
	}
	return forms[best].action, forms[best].method, forms[best].fields, nil
}

func collectGrokFormFields(form *html.Node, mode string, fields url.Values) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node != form && node.Type == html.ElementNode && node.Data == "form" {
			return
		}
		if node.Type == html.ElementNode && !grokHasHTMLAttr(node, "disabled") {
			switch node.Data {
			case "input":
				name := strings.TrimSpace(grokHTMLAttr(node, "name"))
				kind := strings.ToLower(strings.TrimSpace(grokHTMLAttr(node, "type")))
				value := grokHTMLAttr(node, "value")
				if name != "" {
					switch kind {
					case "checkbox", "radio":
						if grokHasHTMLAttr(node, "checked") {
							fields.Add(name, value)
						}
					case "submit", "button", "image":
						if wantedGrokSubmit(mode, name, value, grokHTMLAttr(node, "aria-label")) {
							fields.Set(name, value)
						}
					case "file", "reset":
					default:
						fields.Add(name, value)
					}
				}
			case "button":
				name := strings.TrimSpace(grokHTMLAttr(node, "name"))
				value := grokHTMLAttr(node, "value")
				label := strings.TrimSpace(grokNodeText(node))
				if name != "" && wantedGrokSubmit(mode, name, value, label, grokHTMLAttr(node, "aria-label")) {
					if value == "" {
						value = label
					}
					fields.Set(name, value)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(form)
}

func scoreGrokForm(action string, fields url.Values, mode string) int {
	joined := strings.ToLower(action + " " + fields.Encode())
	score := 0
	if fields.Has("user_code") {
		score += 5
	}
	if mode == "verify" {
		if strings.Contains(joined, "verify") || strings.Contains(joined, "continue") {
			score += 8
		}
		return score
	}
	if strings.Contains(joined, "approve") || strings.Contains(joined, "consent") ||
		strings.Contains(joined, "allow") || fields.Has("principal_id") {
		score += 8
	}
	if fields.Has("state") || fields.Has("csrf") || fields.Has("_csrf") {
		score += 2
	}
	return score
}

func wantedGrokSubmit(mode string, values ...string) bool {
	joined := strings.ToLower(strings.Join(values, " "))
	if mode == "verify" {
		return strings.Contains(joined, "continue") || strings.Contains(joined, "verify") || strings.Contains(joined, "next")
	}
	return strings.Contains(joined, "allow") || strings.Contains(joined, "approve") ||
		strings.Contains(joined, "authorize") || strings.Contains(joined, "accept")
}

func grokHTMLAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func grokHasHTMLAttr(node *html.Node, key string) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return true
		}
	}
	return false
}

func grokNodeText(node *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return b.String()
}

func looksLikeGrokDeviceDone(finalURL string, body []byte) bool {
	value := strings.ToLower(finalURL + " " + string(body))
	for _, marker := range []string{"device/done", "device authorized", "has been authorized", "you can close", "授权成功", "已授权"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func grokSSOStartBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := time.Second << (attempt - 1)
	if backoff > 30*time.Second {
		return 30 * time.Second
	}
	return backoff
}

func grokSleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func safeGrokAuthURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "x.ai" || strings.HasSuffix(host, ".x.ai")
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type grokSSOVerifyOptions struct {
	probe   func(context.Context, string, string, string, bool) error
	convert func(context.Context, string, string, string, bool) (*grokOAuthToken, error)
	sleep   func(context.Context, time.Duration) error
}

func grokRegistrationVerifyContext(cancel *SafeCancel) (context.Context, context.CancelFunc) {
	ctx, cancelContext := context.WithTimeout(context.Background(), grokSSOVerificationTimeout)
	if cancel != nil {
		go func() {
			select {
			case <-cancel.Done():
				cancelContext()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancelContext
}

// grokVerifyRegisteredSSO runs post-register liveness for free SSO accounts.
// Gold standard matches sub2api: session probe + optional device OAuth convert.
//
// verify_status:
//   - oauth_alive: SSO accepted and device OAuth produced access_token (best)
//   - sso_alive:   SSO accepted by accounts.x.ai but OAuth convert failed
//   - dead:        SSO rejected
//   - cancelled:   registration was cancelled while waiting or converting
func grokVerifyRegisteredSSO(ctx context.Context, sso, ssoRw, proxy string, proxyOn bool) (verifyStatus string, oauth *grokOAuthToken, err error) {
	return grokVerifyRegisteredSSOWithOptions(ctx, sso, ssoRw, proxy, proxyOn, grokSSOVerifyOptions{
		probe:   grokProbeSSOSession,
		convert: convertGrokSSOToOAuth,
		sleep:   grokSleepContext,
	})
}

func grokVerifyRegisteredSSOWithOptions(ctx context.Context, sso, ssoRw, proxy string, proxyOn bool, opts grokSSOVerifyOptions) (verifyStatus string, oauth *grokOAuthToken, err error) {
	if opts.probe == nil || opts.convert == nil {
		return "dead", nil, errors.New("incomplete Grok SSO verifier")
	}
	if opts.sleep == nil {
		opts.sleep = grokSleepContext
	}
	probeErr := opts.probe(ctx, sso, ssoRw, proxy, proxyOn)
	if errors.Is(probeErr, errGrokSSOUnauthorized) {
		// A newly-created account can expose the cookie before the session has
		// propagated to accounts.x.ai. Retry that precise state once after a
		// short, cancellable settle; do not retry transport or protocol errors.
		if sleepErr := opts.sleep(ctx, grokSSOPropagationDelay); sleepErr != nil {
			return "cancelled", nil, sleepErr
		}
		probeErr = opts.probe(ctx, sso, ssoRw, proxy, proxyOn)
	}
	if probeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "cancelled", nil, ctxErr
		}
		return "dead", nil, probeErr
	}
	tok, cerr := opts.convert(ctx, sso, ssoRw, proxy, proxyOn)
	if cerr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "cancelled", nil, ctxErr
		}
		return "sso_alive", nil, cerr
	}
	return "oauth_alive", tok, nil
}
