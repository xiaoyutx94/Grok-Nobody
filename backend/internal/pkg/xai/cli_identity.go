package xai

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

// Grok CLI 身份头配置化。
//
// 官方 CLI 是闭源二进制，头集合（identifier / authenticateresponse 等）无法像
// 版本号一样自动跟随（版本号见 cli_version.go，自动拉取 x.ai/cli/stable）。
// 这些身份头通过环境变量暴露给部署层：官方更新头集合后，管理员改环境变量
// 重启容器即可生效，无需修改代码重新编译。
//
// 全部配置项均为进程启动后按需读取（os.Getenv 开销可忽略），测试可直接
// t.Setenv 覆盖，无缓存污染问题。
const (
	// EnvCLIIdentifier 覆盖 x-grok-client-identifier 值（默认 grok-shell）。
	EnvCLIIdentifier = "XAI_GROK_CLI_IDENTIFIER"
	// EnvCLIAuthenticateResponse 覆盖 x-authenticateresponse 值（默认 authenticate-response）。
	EnvCLIAuthenticateResponse = "XAI_GROK_AUTHENTICATE_RESPONSE"
	// EnvCLIStableURL 覆盖官方版本号源（默认 https://x.ai/cli/stable）。
	EnvCLIStableURL = "XAI_GROK_CLI_STABLE_URL"
	// EnvCLIStableBaseline 覆盖版本下限（默认 CLIClientVersion=1.0.0）。
	// 仅在需要临时钉住更高基线时使用（如官方发版异常、被迫跳版本）。
	EnvCLIStableBaseline = "XAI_GROK_CLI_VERSION_BASELINE"
	// EnvCLIExtraHeaders 附加自定义身份头，JSON 对象（如 {"x-grok-region":"us-east-1"}）。
	// 在默认身份头之后应用，可覆盖默认值；解析失败时整体忽略（fail-closed）。
	EnvCLIExtraHeaders = "XAI_GROK_EXTRA_HEADERS"

	defaultCLIIdentifier       = "grok-shell"
	defaultCLIAuthenticateResp = "authenticate-response"
	defaultCLIStableURL        = "https://x.ai/cli/stable"
)

// CLIClientIdentifier 返回 x-grok-client-identifier 值。
func CLIClientIdentifier() string {
	if v := strings.TrimSpace(os.Getenv(EnvCLIIdentifier)); v != "" {
		return v
	}
	return defaultCLIIdentifier
}

// CLIAuthenticateResponse 返回 x-authenticateresponse 值。
func CLIAuthenticateResponse() string {
	if v := strings.TrimSpace(os.Getenv(EnvCLIAuthenticateResponse)); v != "" {
		return v
	}
	return defaultCLIAuthenticateResp
}

// CLIStableVersionURL 返回官方版本号源 URL。
func CLIStableVersionURL() string {
	if v := strings.TrimSpace(os.Getenv(EnvCLIStableURL)); v != "" {
		return v
	}
	return defaultCLIStableURL
}

// CLIStableBaseline 返回版本下限基线。非严格 X.Y.Z semver 或空值回落 CLIClientVersion。
func CLIStableBaseline() string {
	if v := strings.TrimSpace(os.Getenv(EnvCLIStableBaseline)); isStrictSemver(v) {
		return v
	}
	return CLIClientVersion
}

// isStrictSemver 校验严格 X.Y.Z 格式（x/mod/semver 的 IsValid 接受 "v1.0" 这类
// 不完整版本，用于版本下限/基线必须拒绝）。
func isStrictSemver(v string) bool {
	if v == "" || strings.Count(v, ".") != 2 {
		return false
	}
	return semver.IsValid("v" + v)
}

// ExtraGrokCLIHeaders 返回附加身份头。非法 JSON 返回 nil（不注入任何附加头）。
func ExtraGrokCLIHeaders() map[string]string {
	raw := strings.TrimSpace(os.Getenv(EnvCLIExtraHeaders))
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		slog.Warn("xai_extra_headers_invalid_json", "error", err.Error())
		return nil
	}
	return m
}
