package xai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIClientIdentifierDefaults(t *testing.T) {
	t.Setenv(EnvCLIIdentifier, "")
	require.Equal(t, defaultCLIIdentifier, CLIClientIdentifier())
}

func TestCLIClientIdentifierEnvOverride(t *testing.T) {
	t.Setenv(EnvCLIIdentifier, "custom-shell")
	require.Equal(t, "custom-shell", CLIClientIdentifier())
	t.Setenv(EnvCLIIdentifier, "  ")
	require.Equal(t, defaultCLIIdentifier, CLIClientIdentifier())
}

func TestCLIAuthenticateResponseDefaultsAndOverride(t *testing.T) {
	t.Setenv(EnvCLIAuthenticateResponse, "")
	require.Equal(t, defaultCLIAuthenticateResp, CLIAuthenticateResponse())
	t.Setenv(EnvCLIAuthenticateResponse, "auth-resp-v2")
	require.Equal(t, "auth-resp-v2", CLIAuthenticateResponse())
}

func TestCLIStableVersionURLDefaultsAndOverride(t *testing.T) {
	t.Setenv(EnvCLIStableURL, "")
	require.Equal(t, defaultCLIStableURL, CLIStableVersionURL())
	t.Setenv(EnvCLIStableURL, "https://mirror.example.test/cli/stable")
	require.Equal(t, "https://mirror.example.test/cli/stable", CLIStableVersionURL())
}

func TestCLIStableBaseline(t *testing.T) {
	t.Setenv(EnvCLIStableBaseline, "")
	require.Equal(t, CLIClientVersion, CLIStableBaseline())
	// 合法 semver 生效
	t.Setenv(EnvCLIStableBaseline, "1.2.0")
	require.Equal(t, "1.2.0", CLIStableBaseline())
	// 非法 semver 回落基线
	t.Setenv(EnvCLIStableBaseline, "not-a-version")
	require.Equal(t, CLIClientVersion, CLIStableBaseline())
	// 缺 patch 号也回落
	t.Setenv(EnvCLIStableBaseline, "1.0")
	require.Equal(t, CLIClientVersion, CLIStableBaseline())
}

func TestExtraGrokCLIHeaders(t *testing.T) {
	t.Setenv(EnvCLIExtraHeaders, "")
	require.Nil(t, ExtraGrokCLIHeaders())

	t.Setenv(EnvCLIExtraHeaders, `{"x-grok-region":"us-east-1","x-custom":"v1"}`)
	got := ExtraGrokCLIHeaders()
	require.Equal(t, map[string]string{"x-grok-region": "us-east-1", "x-custom": "v1"}, got)

	// 非法 JSON fail-closed:整体不注入
	t.Setenv(EnvCLIExtraHeaders, `{"x-grok-region":`)
	require.Nil(t, ExtraGrokCLIHeaders())
}
