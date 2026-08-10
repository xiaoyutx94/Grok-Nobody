package engine

// 导出格式：对齐 sub2api 与 new-api 的真实导入契约。
//
// 旧实现为什么导不进去：
//   - sub2api 格式导出的是 {"sso_tokens":[...],"count":N}，但 sub2api 的
//     账号导入（POST /admin/accounts/data，前端 ImportDataModal）要求顶层
//     同时存在 proxies 与 accounts 两个数组，且 type ∈
//     {sub2api-data, sub2api-bundle}、version = 1；缺这两个数组直接判「格式错误」。
//   - 而且只给 SSO 等于把已经换到手的 OAuth 凭证丢掉，让 sub2api 重新做一遍
//     SSO→OAuth 转换（慢且会失败）。所以要导「完整授权」。
//   - newapi 格式导出的是自造的 NDJSON，new-api 没有任何入口吃它。
//
// sub2api 侧账号凭证形状（backend/internal/service/grok_oauth_service.go
// BuildAccountCredentials + grok_register_service.go createGrokOAuthAccount）：
//   access_token / refresh_token / id_token / token_type / expires_at(RFC3339)
//   / scope / email / sub / base_url，并额外保留 sso、sso_rw、password 供日后重转。
//   base_url 固定为 CLI 网关。
//
// new-api 侧（QuantumNous/new-api）：
//   渠道类型 xAI = 48，默认 base_url https://api.x.ai —— 但免费账号走它必然
//   402（无 API 额度）。改指 CLI 网关又会 426，因为 new-api 的 xAI 适配器只发
//   Authorization: Bearer，缺 x-grok-client-version。实测：
//     仅 Bearer                        → 426 CLI version (none) is outdated
//     Bearer + x-xai-token-auth        → 426
//     Bearer + x-grok-client-version 等 → 200
//   所以必须借 Channel.header_override 注入 CLI 身份头。

import (
	"encoding/json"
	"fmt"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/umbraforge/desktop/internal/pkg/xai"
)

const (
	// sub2apiDataType / sub2apiDataVersion 必须与 sub2api 的 validateDataHeader
	// 一致，否则前端 isValidDataPayload 直接拒。
	sub2apiDataType    = "sub2api-data"
	sub2apiDataVersion = 1

	// newapiChannelTypeXai 是 new-api 的 xAI 渠道类型 ID。
	newapiChannelTypeXai = 48
)

// sub2apiProxy 对应 sub2api 的 DataProxy。
type sub2apiProxy struct {
	ProxyKey string `json:"proxy_key"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Status   string `json:"status"`
}

// sub2apiAccount 对应 sub2api 的 DataAccount。
type sub2apiAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	ProxyKey    *string        `json:"proxy_key,omitempty"`
	Concurrency int            `json:"concurrency"`
	Priority    int            `json:"priority"`
}

// sub2apiBundle 对应 sub2api 的 DataPayload。
// proxies / accounts 必须是数组而不是 null，否则导入前校验就失败。
type sub2apiBundle struct {
	Type       string           `json:"type"`
	Version    int              `json:"version"`
	ExportedAt string           `json:"exported_at"`
	Proxies    []sub2apiProxy   `json:"proxies"`
	Accounts   []sub2apiAccount `json:"accounts"`
}

// accessTokenExpiresAt 把相对 expires_in 折算成绝对 RFC3339 时间。
// 取 UpdatedAt（拿到 token 的时刻）为基准，缺失则退回 CreatedAt。
// 算不出来就返回空串——sub2api 允许缺 expires_at，会在首次调用时按需刷新。
func accessTokenExpiresAt(a *Account) string {
	secs := anyToSeconds(a.ExpiresIn)
	if secs <= 0 {
		return ""
	}
	base := strings.TrimSpace(a.UpdatedAt)
	if base == "" {
		base = strings.TrimSpace(a.CreatedAt)
	}
	if base == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, base)
	if err != nil {
		return ""
	}
	return t.Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339)
}

// anyToSeconds 兼容 expires_in 的多种落盘类型（JSON 数字会解成 float64）。
func anyToSeconds(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0
		}
		return n
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// grokOAuthCredentials 组装 sub2api 认的 Grok OAuth 凭证。
// 除 OAuth 三件套外保留 sso/sso_rw/password，便于日后在 sub2api 侧重转或改密。
func grokOAuthCredentials(a *Account) map[string]any {
	creds := map[string]any{}
	if v := strings.TrimSpace(a.AccessToken); v != "" {
		creds["access_token"] = v
	}
	if v := strings.TrimSpace(a.RefreshToken); v != "" {
		creds["refresh_token"] = v
	}
	if v := strings.TrimSpace(a.IDToken); v != "" {
		creds["id_token"] = v
	}
	tokenType := strings.TrimSpace(a.TokenType)
	if tokenType == "" && creds["access_token"] != nil {
		tokenType = "Bearer"
	}
	if tokenType != "" {
		creds["token_type"] = tokenType
	}
	if exp := accessTokenExpiresAt(a); exp != "" {
		creds["expires_at"] = exp
	}
	if v := strings.TrimSpace(a.OAuthScope); v != "" {
		creds["scope"] = v
	}
	if v := strings.TrimSpace(a.Email); v != "" {
		creds["email"] = v
	}
	// 备份用：sub2api 的入库流程同样会把这三个存进 credentials
	if v := strings.TrimSpace(a.SSO); v != "" {
		creds["sso"] = v
	}
	if v := strings.TrimSpace(a.SSORW); v != "" {
		creds["sso_rw"] = v
	}
	if a.Password != "" {
		creds["password"] = a.Password
	}
	// 免费账号只有 grok-cli:access，必须钉 CLI 网关，否则 sub2api 侧照样 402
	base := strings.TrimSpace(a.BaseURL)
	if base == "" {
		base = xai.DefaultCLIBaseURL
	}
	creds["base_url"] = strings.TrimSuffix(base, "/")
	return creds
}

// proxyToSub2API 把账号代理拆成 sub2api 的 DataProxy。
// ok=false 表示无代理或无法解析（此时账号按直连导出）。
func proxyToSub2API(raw string) (sub2apiProxy, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sub2apiProxy{}, false
	}
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return sub2apiProxy{}, false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return sub2apiProxy{}, false
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	protocol := strings.ToLower(u.Scheme)
	// sub2api 侧按 protocol|host|port|user|pass 做去重键
	key := fmt.Sprintf("%s|%s|%d|%s|%s", protocol, u.Hostname(), port, user, pass)
	name := fmt.Sprintf("%s:%d", u.Hostname(), port)
	return sub2apiProxy{
		ProxyKey: key,
		Name:     name,
		Protocol: protocol,
		Host:     u.Hostname(),
		Port:     port,
		Username: user,
		Password: pass,
		Status:   "active",
	}, true
}

// buildSub2APIBundle 生成可直接被 sub2api「导入数据」吃下的 JSON。
// includeProxy 为 true 时把账号的注册代理一并带出并绑定。
func buildSub2APIBundle(rows []Account, includeProxy bool) sub2apiBundle {
	bundle := sub2apiBundle{
		Type:       sub2apiDataType,
		Version:    sub2apiDataVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Proxies:    []sub2apiProxy{},
		Accounts:   []sub2apiAccount{},
	}
	seenProxy := map[string]struct{}{}
	for i := range rows {
		a := &rows[i]
		// 没有 access_token 的账号导过去也用不了（sub2api 会当待转协议处理），
		// 这里仍然导出，靠 credentials 里的 sso 让对端补转。
		name := strings.TrimSpace(a.Email)
		if name == "" {
			name = "grok-" + shortID(a.ID)
		}
		acct := sub2apiAccount{
			Name:        name,
			Platform:    "grok",
			Type:        "oauth",
			Credentials: grokOAuthCredentials(a),
		}
		if includeProxy {
			if p, ok := proxyToSub2API(a.Proxy); ok {
				if _, dup := seenProxy[p.ProxyKey]; !dup {
					seenProxy[p.ProxyKey] = struct{}{}
					bundle.Proxies = append(bundle.Proxies, p)
				}
				key := p.ProxyKey
				acct.ProxyKey = &key
			}
		}
		bundle.Accounts = append(bundle.Accounts, acct)
	}
	return bundle
}

// newapiChannel 对应 new-api 的 Channel（仅导入需要的字段）。
type newapiChannel struct {
	Type           int    `json:"type"`
	Name           string `json:"name"`
	Key            string `json:"key"`
	BaseURL        string `json:"base_url"`
	Models         string `json:"models"`
	Group          string `json:"group"`
	Priority       int    `json:"priority"`
	Weight         int    `json:"weight"`
	Status         int    `json:"status"`
	HeaderOverride string `json:"header_override"`
}

// newapiCLIHeaderOverride 注入 CLI 身份头。new-api 的 xAI 适配器只发
// Authorization: Bearer，少了 x-grok-client-version 会被 CLI 网关判
// 426「CLI version (none) is outdated」。
func newapiCLIHeaderOverride() string {
	b, _ := json.Marshal(map[string]string{
		xai.CLITokenAuthHeader:     xai.CLITokenAuthValue,
		xai.CLIClientVersionHeader: xai.CLIClientVersion,
		"User-Agent":               xai.CLIUserAgentString(),
	})
	return string(b)
}

// buildNewAPIChannels 生成 new-api 渠道数组。
// 只导出已有 access_token 的账号——new-api 不会做 SSO→OAuth 转换，
// 只有 SSO 的账号导过去是死的。
func buildNewAPIChannels(rows []Account) []newapiChannel {
	models := strings.Join(xai.DefaultModelIDs(), ",")
	headers := newapiCLIHeaderOverride()
	out := make([]newapiChannel, 0, len(rows))
	for i := range rows {
		a := &rows[i]
		token := strings.TrimSpace(a.AccessToken)
		if token == "" {
			continue
		}
		name := strings.TrimSpace(a.Email)
		if name == "" {
			name = "grok-" + shortID(a.ID)
		}
		base := strings.TrimSpace(a.BaseURL)
		if base == "" {
			base = xai.DefaultCLIBaseURL
		}
		// new-api 的 base_url 是站点根，路径由适配器拼，去掉尾部 /v1
		base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1")
		out = append(out, newapiChannel{
			Type:           newapiChannelTypeXai,
			Name:           name,
			Key:            token,
			BaseURL:        base,
			Models:         models,
			Group:          "default",
			Priority:       0,
			Weight:         1,
			Status:         1,
			HeaderOverride: headers,
		})
	}
	return out
}

// shortID 取 ID 前 8 位做兜底名字（ID 可能短于 8）。
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
