package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/umbraforge/desktop/internal/engine"
	"github.com/umbraforge/desktop/internal/pkg/proxyurl"
	"github.com/umbraforge/desktop/internal/pkg/xai"
)

// 上游转发：grok 官方 CLI 网关身份头 + 分组代理出口 + 流式/非流式透传。
//
// 反代指纹（与 sub2api http_upstream.go applyGrokCLIProxyHeaders 对齐）：
// cli-chat-proxy 网关按「应用层 header 身份」识别客户端，不模拟 TLS 指纹。
// 核心身份头：x-xai-token-auth / x-grok-client-version(动态) / identifier /
// authenticateresponse / UA xai-grok-workspace/<ver>。

// grokCLIProxyHost 仅对该 host 注入 CLI 身份（直连 api.x.ai 保持不变）。
const grokCLIProxyHost = "cli-chat-proxy.grok.com"

// applyGrokGatewayHeaders 注入 grok CLI 应用层身份头（版本动态跟随）。
// 判定：服务类型为 grok（网关配置的 BaseURL）或目标 host 是官方 CLI 网关（双保险）。
func applyGrokGatewayHeaders(req *http.Request, grokService bool) {
	if req == nil || req.URL == nil {
		return
	}
	host := strings.TrimSpace(req.URL.Hostname())
	if !grokService && !strings.EqualFold(host, grokCLIProxyHost) {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	version := xai.EffectiveCLIClientVersion()
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-version", version)
	req.Header.Set("x-grok-client-identifier", xai.CLIClientIdentifier())
	req.Header.Set("x-authenticateresponse", xai.CLIAuthenticateResponse())
	req.Header.Set("User-Agent", "xai-grok-workspace/"+version)
	// 附加身份头（XAI_GROK_EXTRA_HEADERS JSON，核心头受保护已在包内处理）
	for k, v := range xai.ExtraGrokCLIHeaders() {
		req.Header.Set(k, v)
	}
}

// ForwardResult 转发结果。
type ForwardResult struct {
	Status    int
	Header    http.Header
	Body      io.ReadCloser // 调用方负责关闭
	ProxyUsed string        // 实际出口（脱敏），空 = 直连
	AccountID string
}

// forwardUpstream 转发单个请求到上游。
// baseURL: 服务 BaseURL（如 https://cli-chat-proxy.grok.com/v1）
// path:    上游路径（原请求路径去掉 /v1 前缀，如 /responses）
// proxy:   分组绑定代理（空 = 直连）
// grokService: 服务类型为 grok（注入 CLI 身份头）
func (g *GatewayService) forwardUpstream(ctx context.Context, baseURL, path string, method string, body io.Reader, contentType string, accept string, acc engine.Account, proxy string, grokService bool) (*ForwardResult, error) {
	target := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("构造上游请求失败: %w", err)
	}
	if ct := strings.TrimSpace(contentType); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token := strings.TrimSpace(acc.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	applyGrokGatewayHeaders(req, grokService)

	transport := &http.Transport{}
	proxyUsed := ""
	if strings.TrimSpace(proxy) != "" {
		trimmed, parsed, perr := proxyurl.Parse(proxy)
		if perr != nil {
			return nil, fmt.Errorf("分组代理无效: %v", perr)
		}
		if trimmed != "" {
			transport.Proxy = http.ProxyURL(parsed)
			proxyUsed = parsed.Redacted()
		}
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上游请求失败: %w", err)
	}
	return &ForwardResult{
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      resp.Body,
		ProxyUsed: proxyUsed,
		AccountID: acc.ID,
	}, nil
}

// classifyUpstream 判定上游失败类型（封号研判：429=量、403=质）。
func classifyUpstream(status int, errMsg string) (cooldownSeconds int, forbidden bool) {
	msg := strings.ToLower(errMsg)
	switch {
	case status == 403 || strings.Contains(msg, "forbidden") || strings.Contains(msg, "access denied"):
		return 0, true
	case status == 429 || strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests"):
		return int(RateLimitCooldown.Seconds()), false
	case errMsg != "":
		return int(NetworkCooldown.Seconds()), false
	default:
		return 0, false
	}
}

// upstreamURL 组装上游目标 URL（校验合法）。
func upstreamURL(baseURL, path string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("服务地址协议无效: %s", u.Scheme)
	}
	return u, nil
}

// isSSEStream 判断响应是否流式（上游 Content-Type）。
func isSSEStream(h http.Header) bool {
	ct := strings.ToLower(h.Get("Content-Type"))
	return strings.Contains(ct, "text/event-stream")
}
