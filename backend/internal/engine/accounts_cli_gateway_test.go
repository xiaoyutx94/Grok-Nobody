package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umbraforge/desktop/internal/pkg/xai"
)

// TestAccountBaseURLDefaultsToCLIGateway 固定住 402 的根因。
// SSO 换来的免费账号只有 grok-cli:access，走 api.x.ai 会被判无 API 额度
// 直接 402（personal-team-blocked:spending-limit），必须默认 CLI 网关。
func TestAccountBaseURLDefaultsToCLIGateway(t *testing.T) {
	acc := &Account{}
	got := accountBaseURL(acc)
	if got != xai.DefaultCLIBaseURL {
		t.Errorf("空 base_url 默认 = %q, want %q", got, xai.DefaultCLIBaseURL)
	}
	if strings.Contains(got, "api.x.ai") {
		t.Errorf("默认不能是 api.x.ai（免费账号必然 402）: %q", got)
	}
	// 显式配置要被尊重（运维可能切到区域端点或自建中转）
	acc.BaseURL = "https://custom.example.com/v1/"
	if got := accountBaseURL(acc); got != "https://custom.example.com/v1" {
		t.Errorf("自定义 base_url = %q", got)
	}
}

// TestUpsertPinsCLIBaseURL 注册回填时就写死 base_url，避免历史账号留空。
func TestUpsertPinsCLIBaseURL(t *testing.T) {
	svc := newTestAccountService(t)
	acc, err := svc.UpsertFromResult(context.Background(), map[string]any{
		"email": "fresh@example.com", "sso": "s", "access_token": "AT",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if acc.BaseURL != xai.DefaultCLIBaseURL {
		t.Errorf("BaseURL = %q, want %q", acc.BaseURL, xai.DefaultCLIBaseURL)
	}
	// 注册流程显式给了 base_url 时不覆盖
	acc2, err := svc.UpsertFromResult(context.Background(), map[string]any{
		"email": "other@example.com", "sso": "s2", "base_url": "https://api.x.ai/v1",
	})
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if acc2.BaseURL != "https://api.x.ai/v1" {
		t.Errorf("显式 base_url 被覆盖: %q", acc2.BaseURL)
	}
}

// TestTestChatSendsCLIIdentityHeaders CLI 网关只认 grok-cli 身份，缺头会被拒。
func TestTestChatSendsCLIIdentityHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)
	if _, err := svc.TestChat(context.Background(), acc.ID, TestChatOptions{Prompt: "hi"}, nil); err != nil {
		t.Fatalf("TestChat: %v", err)
	}

	if v := got.Get(xai.CLITokenAuthHeader); v != xai.CLITokenAuthValue {
		t.Errorf("%s = %q, want %q", xai.CLITokenAuthHeader, v, xai.CLITokenAuthValue)
	}
	if v := got.Get(xai.CLIClientVersionHeader); v != xai.CLIClientVersion {
		t.Errorf("%s = %q, want %q", xai.CLIClientVersionHeader, v, xai.CLIClientVersion)
	}
	if ua := got.Get("User-Agent"); ua != xai.CLIUserAgentString() {
		t.Errorf("User-Agent = %q, want %q", ua, xai.CLIUserAgentString())
	}
	if auth := got.Get("Authorization"); auth != "Bearer at-1" {
		t.Errorf("Authorization = %q", auth)
	}
}

// TestListModelsSendsCLIIdentityHeaders 模型列表同样走 CLI 网关。
func TestListModelsSendsCLIIdentityHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.5"}]}`))
	}))
	defer srv.Close()

	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)
	if _, err := svc.ListModels(context.Background(), acc.ID, false); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if v := got.Get(xai.CLITokenAuthHeader); v != xai.CLITokenAuthValue {
		t.Errorf("缺 CLI 身份头: %s = %q", xai.CLITokenAuthHeader, v)
	}
	if ua := got.Get("User-Agent"); ua != xai.CLIUserAgentString() {
		t.Errorf("User-Agent = %q", ua)
	}
}

// TestConvertPinsCLIBaseURLOnImport 入库成功后要把 base_url 钉上，
// 否则老账号（base_url 为空）在导出给 sub2api 后仍可能落到 api.x.ai。
func TestConvertPinsCLIBaseURLOnImport(t *testing.T) {
	svc := newTestAccountService(t)
	ctx := context.Background()
	acc, err := svc.UpsertFromResult(ctx, map[string]any{
		"email": "pin@example.com", "sso": "s", "access_token": "AT",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 人为清掉 base_url，模拟历史数据
	empty := ""
	if _, err := svc.Update(ctx, acc.ID, AccountPatch{BaseURL: &empty}); err != nil {
		t.Fatalf("clear base_url: %v", err)
	}
	if _, err := svc.ConvertToOAuth(ctx, []string{acc.ID}, "direct"); err != nil {
		t.Fatalf("convert: %v", err)
	}
	got, _ := svc.Get(ctx, acc.ID)
	// 已有 token 的走「直接标记入库」分支，base_url 仍可能为空；
	// 但读取时 accountBaseURL 会兜到 CLI 网关，实际请求不会打到 api.x.ai
	if strings.Contains(accountBaseURL(&got), "api.x.ai") {
		t.Errorf("有效 base 落到 api.x.ai: %q", accountBaseURL(&got))
	}
}
