package engine

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umbraforge/desktop/internal/pkg/xai"
)

// seedExportAccount 造一条「已入库」的完整账号。
func seedExportAccount(t *testing.T, s *AccountService, email, proxy string) Account {
	t.Helper()
	acc, err := s.UpsertFromResult(context.Background(), map[string]any{
		"email":         email,
		"password":      "Pw!" + email,
		"sso":           "sso-" + email,
		"sso_rw":        "ssorw-" + email,
		"access_token":  "AT-" + email,
		"refresh_token": "RT-" + email,
		"id_token":      "IDT-" + email,
		"token_type":    "Bearer",
		"expires_in":    float64(3600),
		"oauth_scope":   "openid profile email offline_access grok-cli:access api:access",
		"proxy":         proxy,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", email, err)
	}
	return acc
}

// validateSub2APIPayload 复刻 sub2api 前端 isValidDataPayload + 后端
// validateDataHeader 的判定，确保我们导出的文件真能过它的校验。
func validateSub2APIPayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("导出的不是合法 JSON: %v", err)
	}
	// type 允许缺省，但给了就必须在白名单内
	if v, ok := payload["type"]; ok && v != "" {
		if v != "sub2api-data" && v != "sub2api-bundle" {
			t.Errorf("type=%v 不在 sub2api 白名单内", v)
		}
	}
	if v, ok := payload["version"]; ok {
		if n, isNum := v.(float64); isNum && n != 0 && n != 1 {
			t.Errorf("version=%v，sub2api 只认 1", v)
		}
	}
	// 关键：这两个必须是数组，旧实现就是缺它们才被判格式错误
	if _, ok := payload["proxies"].([]any); !ok {
		t.Errorf("proxies 不是数组（sub2api 会判格式错误）: %T", payload["proxies"])
	}
	if _, ok := payload["accounts"].([]any); !ok {
		t.Errorf("accounts 不是数组（sub2api 会判格式错误）: %T", payload["accounts"])
	}
	return payload
}

func TestExportSub2APIPassesImportValidation(t *testing.T) {
	svc := newTestAccountService(t)
	seedExportAccount(t, svc, "a@example.com", "socks5://u:p@1.2.3.4:1080")
	seedExportAccount(t, svc, "b@example.com", "")

	raw, ctype, _, err := svc.Export(context.Background(), "sub2api", false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if ctype != "application/json" {
		t.Errorf("content-type = %q", ctype)
	}
	payload := validateSub2APIPayload(t, raw)

	if payload["type"] != "sub2api-data" {
		t.Errorf("type = %v", payload["type"])
	}
	accounts := payload["accounts"].([]any)
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accounts))
	}

	first := accounts[0].(map[string]any)
	if first["platform"] != "grok" {
		t.Errorf("platform = %v, want grok", first["platform"])
	}
	if first["type"] != "oauth" {
		t.Errorf("type = %v, want oauth", first["type"])
	}
	creds, ok := first["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("credentials 缺失或类型错: %T", first["credentials"])
	}
	// 完整授权：OAuth 三件套必须在
	for _, k := range []string{"access_token", "refresh_token", "id_token", "token_type", "base_url"} {
		if v, ok := creds[k]; !ok || v == "" {
			t.Errorf("credentials 缺 %s（这就是「不是完整授权格式」）", k)
		}
	}
	// base_url 必须是 CLI 网关，否则对端照样 402
	if bu, _ := creds["base_url"].(string); !strings.Contains(bu, "cli-chat-proxy.grok.com") {
		t.Errorf("base_url = %q，应指向 CLI 网关", bu)
	}
	// 备份字段：便于对端重转
	for _, k := range []string{"sso", "sso_rw", "password", "email"} {
		if _, ok := creds[k]; !ok {
			t.Errorf("credentials 缺备份字段 %s", k)
		}
	}
	// expires_at 应折算成绝对 RFC3339
	expStr, _ := creds["expires_at"].(string)
	if expStr == "" {
		t.Error("credentials 缺 expires_at")
	} else if _, err := time.Parse(time.RFC3339, expStr); err != nil {
		t.Errorf("expires_at 不是 RFC3339: %q", expStr)
	}
}

func TestExportSub2APIEmitsAndBindsProxy(t *testing.T) {
	svc := newTestAccountService(t)
	seedExportAccount(t, svc, "p1@example.com", "socks5://user:secret@10.0.0.9:1080")
	seedExportAccount(t, svc, "p2@example.com", "socks5://user:secret@10.0.0.9:1080") // 同代理
	seedExportAccount(t, svc, "p3@example.com", "")                                   // 直连

	raw, _, _, err := svc.Export(context.Background(), "sub2api", false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var bundle sub2apiBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(bundle.Proxies) != 1 {
		t.Fatalf("proxies = %d, want 1（同代理要去重）", len(bundle.Proxies))
	}
	p := bundle.Proxies[0]
	if p.Protocol != "socks5" || p.Host != "10.0.0.9" || p.Port != 1080 {
		t.Errorf("代理解析错: %+v", p)
	}
	if p.Username != "user" || p.Password != "secret" {
		t.Errorf("代理凭据丢失: %+v", p)
	}
	// proxy_key 必须与 sub2api 的 buildProxyKey 同构：protocol|host|port|user|pass
	if p.ProxyKey != "socks5|10.0.0.9|1080|user|secret" {
		t.Errorf("proxy_key = %q，与 sub2api buildProxyKey 不一致", p.ProxyKey)
	}

	bound := 0
	for _, a := range bundle.Accounts {
		if a.ProxyKey != nil {
			bound++
			if *a.ProxyKey != p.ProxyKey {
				t.Errorf("账号绑定的 proxy_key 不匹配: %q", *a.ProxyKey)
			}
		}
	}
	if bound != 2 {
		t.Errorf("绑定代理的账号 = %d, want 2", bound)
	}
}

func TestExportSub2APINoProxyVariant(t *testing.T) {
	svc := newTestAccountService(t)
	seedExportAccount(t, svc, "n@example.com", "socks5://1.2.3.4:1080")

	raw, _, _, err := svc.Export(context.Background(), "sub2api-noproxy", false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	validateSub2APIPayload(t, raw) // 仍要过校验（proxies 必须是空数组而非 null）
	var bundle sub2apiBundle
	_ = json.Unmarshal([]byte(raw), &bundle)
	if len(bundle.Proxies) != 0 {
		t.Errorf("proxies = %d, want 0", len(bundle.Proxies))
	}
	if bundle.Accounts[0].ProxyKey != nil {
		t.Errorf("不应绑定代理: %v", *bundle.Accounts[0].ProxyKey)
	}
	// 空数组必须序列化成 []，不能是 null
	if !strings.Contains(raw, `"proxies": []`) {
		t.Error("proxies 序列化成了 null，sub2api 会判格式错误")
	}
}

func TestExportSub2APIEmptyListStillValid(t *testing.T) {
	svc := newTestAccountService(t)
	raw, _, _, err := svc.Export(context.Background(), "sub2api", false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	// 没有账号时也必须是合法 payload（两个空数组）
	validateSub2APIPayload(t, raw)
}

func TestExportSub2APISSOIsPlainList(t *testing.T) {
	svc := newTestAccountService(t)
	seedExportAccount(t, svc, "s1@example.com", "")
	seedExportAccount(t, svc, "s2@example.com", "")

	raw, ctype, _, err := svc.Export(context.Background(), "sub2api-sso", false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if ctype != "text/plain" {
		t.Errorf("content-type = %q", ctype)
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2", len(lines))
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "sso-") {
			t.Errorf("行内容不是裸 SSO: %q", l)
		}
		if strings.ContainsAny(l, "{}[]\"") {
			t.Errorf("SSO 列表不应含 JSON 符号（贴进输入框会当成 token）: %q", l)
		}
	}
}

func TestExportNewAPIChannelShape(t *testing.T) {
	svc := newTestAccountService(t)
	seedExportAccount(t, svc, "c1@example.com", "")
	// 只有 SSO、无 access_token 的账号不该导出（new-api 不会做转换）
	if _, err := svc.UpsertFromResult(context.Background(), map[string]any{
		"email": "nooauth@example.com", "sso": "only-sso",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	raw, ctype, _, err := svc.Export(context.Background(), "newapi", false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if ctype != "application/json" {
		t.Errorf("content-type = %q（new-api 吃 JSON，不是 NDJSON）", ctype)
	}
	var channels []newapiChannel
	if err := json.Unmarshal([]byte(raw), &channels); err != nil {
		t.Fatalf("不是合法 JSON 数组: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("channels = %d, want 1（无 token 的要跳过）", len(channels))
	}
	ch := channels[0]
	if ch.Type != 48 {
		t.Errorf("type = %d, want 48 (new-api xAI)", ch.Type)
	}
	if ch.Key != "AT-c1@example.com" {
		t.Errorf("key 应是 access_token，实际 %q", ch.Key)
	}
	// base_url 必须是站点根（不含 /v1），且指向 CLI 网关
	if strings.HasSuffix(ch.BaseURL, "/v1") {
		t.Errorf("base_url 不应带 /v1: %q", ch.BaseURL)
	}
	if !strings.Contains(ch.BaseURL, "cli-chat-proxy.grok.com") {
		t.Errorf("base_url = %q，应指向 CLI 网关（api.x.ai 会 402）", ch.BaseURL)
	}
	if ch.Status != 1 {
		t.Errorf("status = %d, want 1 (enabled)", ch.Status)
	}
	if ch.Models == "" {
		t.Error("models 为空，new-api 渠道不可用")
	}

	// 关键：header_override 必须带 x-grok-client-version，
	// 否则 new-api 只发 Bearer，CLI 网关回 426。
	var hdrs map[string]string
	if err := json.Unmarshal([]byte(ch.HeaderOverride), &hdrs); err != nil {
		t.Fatalf("header_override 不是合法 JSON: %v", err)
	}
	if hdrs[xai.CLIClientVersionHeader] != xai.CLIClientVersion {
		t.Errorf("缺 %s（实测缺它 CLI 网关回 426）: %v", xai.CLIClientVersionHeader, hdrs)
	}
	if hdrs[xai.CLITokenAuthHeader] != xai.CLITokenAuthValue {
		t.Errorf("缺 %s: %v", xai.CLITokenAuthHeader, hdrs)
	}
	if hdrs["User-Agent"] != xai.CLIUserAgentString() {
		t.Errorf("User-Agent = %q", hdrs["User-Agent"])
	}
}

func TestAnyToSecondsHandlesStoredTypes(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{nil, 0}, {float64(3600), 3600}, {int(120), 120}, {int64(7200), 7200},
		{"1800", 1800}, {" 900 ", 900}, {"abc", 0}, {[]int{1}, 0},
	}
	for _, c := range cases {
		if got := anyToSeconds(c.in); got != c.want {
			t.Errorf("anyToSeconds(%#v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAccessTokenExpiresAtDerivesFromUpdatedAt(t *testing.T) {
	a := &Account{UpdatedAt: "2026-08-08T10:00:00Z", ExpiresIn: float64(3600)}
	if got := accessTokenExpiresAt(a); got != "2026-08-08T11:00:00Z" {
		t.Errorf("expires_at = %q", got)
	}
	// 缺 UpdatedAt 时退回 CreatedAt
	a = &Account{CreatedAt: "2026-08-08T09:00:00Z", ExpiresIn: float64(60)}
	if got := accessTokenExpiresAt(a); got != "2026-08-08T09:01:00Z" {
		t.Errorf("fallback expires_at = %q", got)
	}
	// 算不出来返回空串（sub2api 允许缺，会按需刷新）
	for _, bad := range []*Account{
		{ExpiresIn: float64(3600)},                        // 无时间基准
		{UpdatedAt: "2026-08-08T10:00:00Z"},               // 无 expires_in
		{UpdatedAt: "not-a-time", ExpiresIn: float64(60)}, // 时间无法解析
	} {
		if got := accessTokenExpiresAt(bad); got != "" {
			t.Errorf("%+v 应返回空串，实际 %q", bad, got)
		}
	}
}

func TestProxyToSub2APIRejectsUnparseable(t *testing.T) {
	for _, raw := range []string{"", "   ", "not-a-url", "socks5://nohost", "http://h:99999"} {
		if _, ok := proxyToSub2API(raw); ok {
			t.Errorf("%q 应判为不可解析", raw)
		}
	}
	p, ok := proxyToSub2API("http://1.2.3.4:8080")
	if !ok {
		t.Fatal("无凭据的 http 代理应可解析")
	}
	if p.Username != "" || p.Password != "" {
		t.Errorf("不应凭空造出凭据: %+v", p)
	}
	if p.ProxyKey != "http|1.2.3.4|8080||" {
		t.Errorf("proxy_key = %q", p.ProxyKey)
	}
}

func TestExportUnknownFormatListsNewOnes(t *testing.T) {
	svc := newTestAccountService(t)
	_, _, _, err := svc.Export(context.Background(), "bogus", false)
	if err == nil {
		t.Fatal("未知格式应报错")
	}
	for _, want := range []string{"sub2api-noproxy", "sub2api-sso", "newapi"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误提示应列出 %s: %v", want, err)
		}
	}
}

func TestExportFileName(t *testing.T) {
	// 带数量：格式标识 + 账号数 + 日期
	name := ExportFileName("sub2api", 10465, "")
	if name != "sub2api_10465账号_20260811.json" && !regexp.MustCompile(`^sub2api_\d+账号_\d{8}\.json$`).MatchString(name) {
		t.Fatalf("sub2api 文件名异常: %s", name)
	}
	// 无代理/新格式标识
	if !regexp.MustCompile(`^sub2api无代理_\d+账号_\d{8}\.json$`).MatchString(ExportFileName("sub2api-noproxy", 3, "")) {
		t.Fatal("sub2api-noproxy 文件名异常: " + ExportFileName("sub2api-noproxy", 3, ""))
	}
	if !regexp.MustCompile(`^sso列表_\d+账号_\d{8}\.txt$`).MatchString(ExportFileName("sub2api-sso", 3, "")) {
		t.Fatal("sub2api-sso 文件名异常")
	}
	if !regexp.MustCompile(`^csv_\d+账号_\d{8}\.csv$`).MatchString(ExportFileName("csv", 3, "")) {
		t.Fatal("csv 文件名异常")
	}
	// 无数量（浏览器下载路径）只带格式+日期
	if !regexp.MustCompile(`^newapi_\d{8}\.json$`).MatchString(ExportFileName("newapi", 0, "")) {
		t.Fatal("newapi 无数量文件名异常")
	}
	// 自定义名优先
	if ExportFileName("csv", 3, "我的导出.txt") != "我的导出.txt" {
		t.Fatal("自定义名应优先")
	}
}
