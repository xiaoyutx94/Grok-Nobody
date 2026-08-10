package grokregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelectGrokHTMLFormPreservesServedConsentState(t *testing.T) {
	body := []byte(`<html><form method="post" action="https://auth.x.ai/oauth2/device/approve-v2">
		<input type="hidden" name="user_code" value="CODE-1">
		<input type="hidden" name="state" value="state-1">
		<input type="hidden" name="principal_id" value="principal-1">
		<button name="action" value="deny">Deny</button>
		<button name="action" value="allow">Allow</button>
	</form></html>`)

	action, method, fields, err := selectGrokHTMLForm(body, "https://accounts.x.ai/oauth2/device/consent", "approve")

	require.NoError(t, err)
	require.Equal(t, "https://auth.x.ai/oauth2/device/approve-v2", action)
	require.Equal(t, http.MethodPost, method)
	require.Equal(t, "CODE-1", fields.Get("user_code"))
	require.Equal(t, "state-1", fields.Get("state"))
	require.Equal(t, "principal-1", fields.Get("principal_id"))
	require.Equal(t, "allow", fields.Get("action"))
}

func TestSelectGrokHTMLFormKeepsHighestScoringCandidate(t *testing.T) {
	body := []byte(`<html>
		<form method="post" action="/oauth2/device/verify"><input name="user_code" value="CODE"><input name="state" value="right"></form>
		<form method="post" action="/newsletter"><input name="user_code" value="WRONG"></form>
	</html>`)

	action, _, fields, err := selectGrokHTMLForm(body, "https://accounts.x.ai/oauth2/device", "verify")

	require.NoError(t, err)
	require.Equal(t, "https://accounts.x.ai/oauth2/device/verify", action)
	require.Equal(t, "right", fields.Get("state"))
}

func TestGrokSSODeviceCookieJarScopesAndFiltersCookies(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	flow := &grokSSODeviceFlow{jar: jar}
	flow.seedSSOCookies("sso-token", "rw-token")
	flow.captureCookies("https://accounts.x.ai/", &Response{Headers: http.Header{"Set-Cookie": {
		"session=shared; Domain=x.ai; Path=/; Secure",
		"host_only=drop; Path=/; Secure",
		"tracking=drop; Domain=x.ai; Path=/; Secure",
	}}})

	cookie := flow.cookieHeader("https://auth.x.ai/oauth2/device/code")
	require.Contains(t, cookie, "sso=sso-token")
	require.Contains(t, cookie, "sso-rw=rw-token")
	require.Contains(t, cookie, "session=shared")
	require.NotContains(t, cookie, "host_only=")
	require.NotContains(t, cookie, "tracking=")
}

func TestAllowedGrokXAISessionCookie(t *testing.T) {
	tests := []struct {
		name    string
		allowed bool
	}{
		{name: "sso", allowed: true},
		{name: "sso-rw", allowed: true},
		{name: "session", allowed: true},
		{name: "xai_session", allowed: true},
		{name: "oauth_session", allowed: true},
		{name: "oauth_state", allowed: true},
		{name: "ory_kratos_session", allowed: true},
		{name: "ory_hydra_consent_csrf", allowed: true},
		{name: "cf_clearance", allowed: false},
		{name: "__cf_bm", allowed: false},
		{name: "_cfuvid", allowed: false},
		{name: "tracking", allowed: false},
		{name: "analytics_session", allowed: false},
		{name: "__Host-arbitrary", allowed: false},
		{name: "oauth_arbitrary", allowed: false},
		{name: "xai-arbitrary", allowed: false},
		{name: "ory_arbitrary", allowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.allowed, allowedGrokXAISessionCookie(tt.name))
		})
	}
}

type grokSSOPollFake struct {
	body    string
	headers OrderedHeaders
}

func (f *grokSSOPollFake) GetOrdered(string, OrderedHeaders) (*Response, error) {
	return &Response{StatusCode: http.StatusOK, Headers: http.Header{}}, nil
}

func (f *grokSSOPollFake) PostRawOrdered(rawURL string, headers OrderedHeaders, body []byte) (*Response, error) {
	if rawURL == grokSSOTokenURL {
		f.headers = headers.Clone()
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		if values.Get("device_code") != "device-1" {
			return &Response{StatusCode: http.StatusBadRequest, Headers: http.Header{}, Body: []byte(`{"error":"bad_device"}`)}, nil
		}
	}
	return &Response{StatusCode: http.StatusOK, Headers: http.Header{}, Body: []byte(f.body)}, nil
}

func TestGrokSSODevicePollRequiresRefreshToken(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	fake := &grokSSOPollFake{body: `{"access_token":"access-only","expires_in":3600}`}
	flow := &grokSSODeviceFlow{
		client: fake,
		jar:    jar,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	flow.seedSSOCookies("sso", "rw")

	_, err = flow.pollToken(context.Background(), "device-1", time.Second, time.Minute)

	require.ErrorContains(t, err, "no refresh token")
	require.Empty(t, fake.headers.Get("Origin"))
	require.Empty(t, fake.headers.Get("Referer"))
}

func TestGrokSSODevicePollReturnsRenewableToken(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	fake := &grokSSOPollFake{body: `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`}
	flow := &grokSSODeviceFlow{
		client: fake,
		jar:    jar,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	flow.seedSSOCookies("sso", "rw")

	token, err := flow.pollToken(context.Background(), "device-1", time.Second, time.Minute)

	require.NoError(t, err)
	require.Equal(t, "access", token.AccessToken)
	require.Equal(t, "refresh", token.RefreshToken)
	require.False(t, strings.Contains(fake.headers.Get("Cookie"), "tracking="))
}

type grokSSOFlowFake struct {
	attempt      int
	grantDenied  bool
	grantReason  string
	deviceCodes  *[]string
	deviceScopes *[]string
	tokenCookies *[]string
}

func (f *grokSSOFlowFake) GetOrdered(rawURL string, _ OrderedHeaders) (*Response, error) {
	switch {
	case rawURL == grokSSOAccountsURL:
		return &Response{StatusCode: http.StatusOK, Headers: http.Header{"Set-Cookie": {
			fmt.Sprintf("session=session-%d; Domain=x.ai; Path=/; Secure", f.attempt),
			fmt.Sprintf("tracking=tracking-%d; Domain=x.ai; Path=/; Secure", f.attempt),
		}}}, nil
	case strings.Contains(rawURL, "/oauth2/device?"):
		return &Response{StatusCode: http.StatusOK, Headers: http.Header{}, Body: []byte(`<form method="post" action="https://auth.x.ai/oauth2/device/verify"><input name="user_code"><input name="state" value="verify-state"></form>`)}, nil
	default:
		return nil, fmt.Errorf("unexpected GET %s", rawURL)
	}
}

func (f *grokSSOFlowFake) PostRawOrdered(rawURL string, headers OrderedHeaders, body []byte) (*Response, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	switch rawURL {
	case grokSSODeviceURL:
		deviceCode := fmt.Sprintf("device-%d", f.attempt)
		*f.deviceCodes = append(*f.deviceCodes, deviceCode)
		if f.deviceScopes != nil {
			*f.deviceScopes = append(*f.deviceScopes, values.Get("scope"))
		}
		return &Response{StatusCode: http.StatusOK, Headers: http.Header{}, Body: []byte(fmt.Sprintf(
			`{"device_code":%q,"user_code":%q,"verification_uri_complete":%q,"interval":1,"expires_in":60}`,
			deviceCode, fmt.Sprintf("user-%d", f.attempt), fmt.Sprintf("https://accounts.x.ai/oauth2/device?user_code=user-%d", f.attempt),
		))}, nil
	case grokSSOVerifyURL:
		return &Response{StatusCode: http.StatusOK, Headers: http.Header{}, Body: []byte(`<form method="post" action="https://auth.x.ai/oauth2/device/approve"><input name="user_code"><input name="state" value="approve-state"><button name="action" value="allow">Allow</button></form>`)}, nil
	case grokSSOApproveURL:
		return &Response{StatusCode: http.StatusOK, Headers: http.Header{}, Body: []byte(`device authorized`)}, nil
	case grokSSOTokenURL:
		*f.tokenCookies = append(*f.tokenCookies, headers.Get("Cookie"))
		if values.Get("device_code") != fmt.Sprintf("device-%d", f.attempt) {
			return nil, fmt.Errorf("wrong device code %q", values.Get("device_code"))
		}
		if f.grantDenied {
			reason := f.grantReason
			if reason == "" {
				reason = "Access denied"
			}
			return &Response{StatusCode: http.StatusBadRequest, Headers: http.Header{}, Body: []byte(fmt.Sprintf(`{"error":"invalid_grant","error_description":%q}`, reason))}, nil
		}
		return &Response{StatusCode: http.StatusOK, Headers: http.Header{}, Body: []byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)}, nil
	default:
		return nil, fmt.Errorf("unexpected POST %s", rawURL)
	}
}

func TestConvertGrokSSOToOAuthFirstFlowUsesFullScope(t *testing.T) {
	var factoryCalls int
	var deviceCodes, deviceScopes, tokenCookies []string
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			factoryCalls++
			return &grokSSOFlowFake{
				attempt: factoryCalls, deviceCodes: &deviceCodes, deviceScopes: &deviceScopes, tokenCookies: &tokenCookies,
			}, func() {}
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	}

	token, err := convertGrokSSOToOAuthWithOptions(context.Background(), "sso", "rw", opts)

	require.NoError(t, err)
	require.Equal(t, "access", token.AccessToken)
	require.Equal(t, "refresh", token.RefreshToken)
	require.Equal(t, 1, factoryCalls)
	require.Equal(t, []string{grokSSOBuildScope}, deviceScopes)
}

func TestConvertGrokSSOToOAuthReopensFreshFlowAfterStaleGrant(t *testing.T) {
	var factoryCalls int
	var deviceCodes, deviceScopes, tokenCookies []string
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			factoryCalls++
			return &grokSSOFlowFake{
				attempt:      factoryCalls,
				grantDenied:  factoryCalls == 1,
				deviceCodes:  &deviceCodes,
				deviceScopes: &deviceScopes,
				tokenCookies: &tokenCookies,
			}, func() {}
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	}

	token, err := convertGrokSSOToOAuthWithOptions(context.Background(), "sso", "rw", opts)

	require.NoError(t, err)
	require.Equal(t, "refresh", token.RefreshToken)
	require.Equal(t, 2, factoryCalls)
	require.Equal(t, []string{"device-1", "device-2"}, deviceCodes)
	require.Equal(t, []string{grokSSOBuildScope, grokSSOMinimalScope}, deviceScopes)
	require.Len(t, tokenCookies, 2)
	require.Contains(t, tokenCookies[0], "session=session-1")
	require.NotContains(t, tokenCookies[0], "tracking=")
	require.Contains(t, tokenCookies[1], "session=session-2")
	require.NotContains(t, tokenCookies[1], "session=session-1")
}

func TestConvertGrokSSOToOAuthStopsAtStaleGrantLimit(t *testing.T) {
	var factoryCalls int
	var deviceCodes, deviceScopes, tokenCookies []string
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			factoryCalls++
			return &grokSSOFlowFake{
				attempt: factoryCalls, grantDenied: true, deviceCodes: &deviceCodes, deviceScopes: &deviceScopes, tokenCookies: &tokenCookies,
			}, func() {}
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	}

	_, err := convertGrokSSOToOAuthWithOptions(context.Background(), "sso", "rw", opts)

	require.ErrorIs(t, err, ErrGrokSSOAuthorizationDenied)
	require.ErrorContains(t, err, "after 2 fresh device flows")
	require.Equal(t, grokSSOGrantMaxAttempts, factoryCalls)
	require.Equal(t, []string{grokSSOBuildScope, grokSSOMinimalScope}, deviceScopes)
}

func TestConvertGrokSSOToOAuthCancellationDoesNotReopen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var factoryCalls int
	var deviceCodes, deviceScopes, tokenCookies []string
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			factoryCalls++
			return &grokSSOFlowFake{
				attempt: factoryCalls, grantDenied: true, deviceCodes: &deviceCodes, deviceScopes: &deviceScopes, tokenCookies: &tokenCookies,
			}, func() {}
		},
		sleep: func(_ context.Context, delay time.Duration) error {
			if delay == grokSSOGrantRetryDelay {
				cancel()
				return context.Canceled
			}
			return nil
		},
	}

	_, err := convertGrokSSOToOAuthWithOptions(ctx, "sso", "rw", opts)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, factoryCalls)
	require.Equal(t, []string{grokSSOBuildScope}, deviceScopes)
}

func TestConvertGrokSSOToOAuthPermanentGrantErrorDoesNotReopen(t *testing.T) {
	var factoryCalls int
	var deviceCodes, deviceScopes, tokenCookies []string
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			factoryCalls++
			return &grokSSOFlowFake{
				attempt: factoryCalls, grantDenied: true, grantReason: "device code expired",
				deviceCodes: &deviceCodes, deviceScopes: &deviceScopes, tokenCookies: &tokenCookies,
			}, func() {}
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	}

	_, err := convertGrokSSOToOAuthWithOptions(context.Background(), "sso", "rw", opts)

	require.ErrorIs(t, err, ErrGrokSSOAuthorizationDenied)
	require.Equal(t, 1, factoryCalls)
	require.Equal(t, []string{grokSSOBuildScope}, deviceScopes)
}

type grokSSONetworkErrorFake struct {
	err error
}

func (f *grokSSONetworkErrorFake) GetOrdered(string, OrderedHeaders) (*Response, error) {
	return nil, f.err
}

func (f *grokSSONetworkErrorFake) PostRawOrdered(string, OrderedHeaders, []byte) (*Response, error) {
	return nil, f.err
}

func TestConvertGrokSSOToOAuthNetworkErrorDoesNotReopen(t *testing.T) {
	networkErr := errors.New("network unavailable")
	var factoryCalls int
	opts := grokSSOConvertOptions{
		newClient: func() (grokSSODeviceHTTPClient, func()) {
			factoryCalls++
			return &grokSSONetworkErrorFake{err: networkErr}, func() {}
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	}

	_, err := convertGrokSSOToOAuthWithOptions(context.Background(), "sso", "rw", opts)

	require.ErrorIs(t, err, networkErr)
	require.Equal(t, 1, factoryCalls)
}

func TestGrokVerifyRegisteredSSORetriesOnlyUnauthorizedPropagation(t *testing.T) {
	var probes, converts, sleeps int
	want := &grokOAuthToken{AccessToken: "access", RefreshToken: "refresh"}
	opts := grokSSOVerifyOptions{
		probe: func(context.Context, string, string, string, bool) error {
			probes++
			if probes == 1 {
				return errGrokSSOUnauthorized
			}
			return nil
		},
		convert: func(context.Context, string, string, string, bool) (*grokOAuthToken, error) {
			converts++
			return want, nil
		},
		sleep: func(context.Context, time.Duration) error {
			sleeps++
			return nil
		},
	}

	status, token, err := grokVerifyRegisteredSSOWithOptions(context.Background(), "sso", "rw", "", false, opts)

	require.NoError(t, err)
	require.Equal(t, "oauth_alive", status)
	require.Same(t, want, token)
	require.Equal(t, 2, probes)
	require.Equal(t, 1, converts)
	require.Equal(t, 1, sleeps)
}

func TestGrokVerifyRegisteredSSOCancelDuringSettle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var converts int
	opts := grokSSOVerifyOptions{
		probe: func(context.Context, string, string, string, bool) error { return errGrokSSOUnauthorized },
		convert: func(context.Context, string, string, string, bool) (*grokOAuthToken, error) {
			converts++
			return nil, errors.New("must not convert")
		},
		sleep: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	}

	status, _, err := grokVerifyRegisteredSSOWithOptions(ctx, "sso", "rw", "", false, opts)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "cancelled", status)
	require.Zero(t, converts)
}

func TestGrokVerifyRegisteredSSODoesNotRetryNetworkError(t *testing.T) {
	var probes, sleeps int
	opts := grokSSOVerifyOptions{
		probe: func(context.Context, string, string, string, bool) error {
			probes++
			return errors.New("network unavailable")
		},
		convert: func(context.Context, string, string, string, bool) (*grokOAuthToken, error) {
			return nil, errors.New("must not convert")
		},
		sleep: func(context.Context, time.Duration) error {
			sleeps++
			return nil
		},
	}

	status, _, err := grokVerifyRegisteredSSOWithOptions(context.Background(), "sso", "rw", "", false, opts)

	require.ErrorContains(t, err, "network unavailable")
	require.Equal(t, "dead", status)
	require.Equal(t, 1, probes)
	require.Zero(t, sleeps)
}

func TestGrokSSOVerificationTimeoutCoversConversionBudget(t *testing.T) {
	require.Greater(t, grokSSOVerificationTimeout, SSOConversionTimeout)
	require.Greater(t, SSOConversionTimeout, 150*time.Second)
}
