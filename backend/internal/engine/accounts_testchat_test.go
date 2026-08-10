package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umbraforge/desktop/internal/store"
)

func newTestAccountService(t *testing.T) *AccountService {
	t.Helper()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return NewAccountService(st)
}

// seedAccount 插入一条带 OAuth 凭证、base 指向 fake 上游的账号。
func seedAccount(t *testing.T, s *AccountService, base string) Account {
	t.Helper()
	acc, err := s.UpsertFromResult(context.Background(), map[string]any{
		"email":        "a@example.com",
		"password":     "pw",
		"sso":          "sso-token",
		"access_token": "at-1",
		"base_url":     base,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return acc
}

func TestTestChatStreamAggregatesDeltasAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("auth = %q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("stream flag = %v", body["stream"])
		}
		if body["model"] != "grok-4.3" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"choices":[{"delta":{"content":"你好"}}]}`,
			`{"choices":[{"delta":{"content":"，世界"}}]}`,
			`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
		} {
			_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)

	var deltas []string
	res, err := svc.TestChat(context.Background(), acc.ID, TestChatOptions{
		Model: "grok-4.3", Prompt: "hi", Stream: true,
	}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("TestChat: %v", err)
	}
	if res.Reply != "你好，世界" {
		t.Errorf("reply = %q", res.Reply)
	}
	if strings.Join(deltas, "") != "你好，世界" {
		t.Errorf("deltas = %v", deltas)
	}
	if res.TotalTokens != 10 || res.PromptTokens != 7 || res.CompletionTokens != 3 {
		t.Errorf("usage = %+v", res)
	}

	// 成功后写回 ok 留痕
	got, err := svc.Get(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastTestStatus != "ok" || got.LastTestAt == "" || got.LastTestModel != "grok-4.3" {
		t.Errorf("test trace = %+v", got)
	}
	if got.LastTestError != "" {
		t.Errorf("unexpected error trace %q", got.LastTestError)
	}
}

func TestTestChatNonStreamUsesDefaultPromptAndModel(t *testing.T) {
	var gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		if len(body.Messages) > 0 {
			gotPrompt = body.Messages[0].Content
		}
		if body.Stream {
			t.Errorf("expected non-stream")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" hello "}}],"usage":{"total_tokens":5}}`))
	}))
	defer srv.Close()

	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)

	res, err := svc.TestChat(context.Background(), acc.ID, TestChatOptions{}, nil)
	if err != nil {
		t.Fatalf("TestChat: %v", err)
	}
	if res.Reply != "hello" {
		t.Errorf("reply = %q (want trimmed)", res.Reply)
	}
	if gotModel != DefaultTestModel {
		t.Errorf("model = %q, want default", gotModel)
	}
	if gotPrompt != DefaultTestPrompt {
		t.Errorf("prompt = %q, want default", gotPrompt)
	}
	if res.Model != DefaultTestModel {
		t.Errorf("result model = %q", res.Model)
	}
}

func TestTestChatRecordsUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
	}))
	defer srv.Close()

	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)

	if _, err := svc.TestChat(context.Background(), acc.ID, TestChatOptions{Prompt: "x"}, nil); err == nil {
		t.Fatal("expected error on HTTP 401")
	}
	got, _ := svc.Get(context.Background(), acc.ID)
	if got.LastTestStatus != "fail" {
		t.Errorf("status = %q, want fail", got.LastTestStatus)
	}
	if !strings.Contains(got.LastTestError, "401") {
		t.Errorf("error trace = %q, want it to mention 401", got.LastTestError)
	}
}

func TestTestChatRejectsAccountWithoutOAuth(t *testing.T) {
	svc := newTestAccountService(t)
	acc, err := svc.UpsertFromResult(context.Background(), map[string]any{
		"email": "b@example.com",
		"sso":   "only-sso",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = svc.TestChat(context.Background(), acc.ID, TestChatOptions{Prompt: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "OAuth") {
		t.Fatalf("err = %v, want OAuth complaint", err)
	}
}

func TestTestChatEmptyReplyIsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)

	if _, err := svc.TestChat(context.Background(), acc.ID, TestChatOptions{Prompt: "x"}, nil); err == nil {
		t.Fatal("expected error for empty reply")
	}
	got, _ := svc.Get(context.Background(), acc.ID)
	if got.LastTestStatus != "fail" {
		t.Errorf("status = %q", got.LastTestStatus)
	}
}

func TestListModelsParsesUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.5","display_name":"Grok 4.5"},{"id":""},{"id":"grok-4.3"}]}`))
	}))
	defer srv.Close()
	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)

	models, err := svc.ListModels(context.Background(), acc.ID, false)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "grok-4.5" || models[1].ID != "grok-4.3" {
		t.Fatalf("models = %+v (blank id must be dropped)", models)
	}
}

func TestVerifyCredentialRecordsTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.5"}]}`))
	}))
	defer srv.Close()
	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, srv.URL)

	n, err := svc.VerifyCredential(context.Background(), acc.ID, false)
	if err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
	if n != 1 {
		t.Errorf("models = %d", n)
	}
	got, _ := svc.Get(context.Background(), acc.ID)
	if got.LastTestStatus != "ok" || got.LastTestModel != "credential" {
		t.Errorf("trace = %+v", got)
	}
}

func TestUpdateOnlyTouchesProvidedFields(t *testing.T) {
	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, "https://api.x.ai/v1")

	note := "备注A"
	imported := true
	updated, err := svc.Update(context.Background(), acc.ID, AccountPatch{Note: &note, Imported: &imported})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Note != "备注A" || !updated.Imported {
		t.Errorf("patch not applied: %+v", updated)
	}
	if updated.Email != acc.Email || updated.Password != acc.Password || updated.AccessToken != acc.AccessToken {
		t.Errorf("untouched fields changed: %+v", updated)
	}

	// 置空需要显式传空串，且不影响其他字段
	empty := ""
	updated, err = svc.Update(context.Background(), acc.ID, AccountPatch{Note: &empty})
	if err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	if updated.Note != "" {
		t.Errorf("note not cleared: %q", updated.Note)
	}
	if !updated.Imported {
		t.Errorf("imported should stay true when not provided")
	}
}

func TestUpdateRejectsInvalidProxy(t *testing.T) {
	svc := newTestAccountService(t)
	acc := seedAccount(t, svc, "https://api.x.ai/v1")

	bad := "ftp://1.2.3.4:1080"
	if _, err := svc.Update(context.Background(), acc.ID, AccountPatch{Proxy: &bad}); err == nil {
		t.Fatal("expected rejection of unsupported scheme")
	}
	// 确认没写坏
	got, _ := svc.Get(context.Background(), acc.ID)
	if got.Proxy != "" {
		t.Errorf("proxy persisted despite error: %q", got.Proxy)
	}

	// socks5 会被规范化为 socks5h（防 DNS 泄漏）
	ok := "socks5://1.2.3.4:1080"
	updated, err := svc.Update(context.Background(), acc.ID, AccountPatch{Proxy: &ok})
	if err != nil {
		t.Fatalf("Update proxy: %v", err)
	}
	if !strings.HasPrefix(updated.Proxy, "socks5h://") {
		t.Errorf("proxy = %q, want socks5h normalization", updated.Proxy)
	}
}

func TestUpdateRejectsUnknownAccount(t *testing.T) {
	svc := newTestAccountService(t)
	note := "x"
	if _, err := svc.Update(context.Background(), "nope", AccountPatch{Note: &note}); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestUpsertPreservesTestTrace(t *testing.T) {
	svc := newTestAccountService(t)
	ctx := context.Background()
	acc := seedAccount(t, svc, "https://api.x.ai/v1")
	svc.recordTest(ctx, acc.ID, "grok-4.5", 123, nil)

	// 同邮箱再次入库（注册回填），留痕不能被清掉
	if _, err := svc.UpsertFromResult(ctx, map[string]any{
		"email":        "a@example.com",
		"sso":          "sso-token-2",
		"access_token": "at-2",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ := svc.Get(ctx, acc.ID)
	if got.LastTestStatus != "ok" || got.LastTestMs != 123 || got.LastTestModel != "grok-4.5" {
		t.Errorf("trace lost after re-upsert: %+v", got)
	}
	if got.AccessToken != "at-2" {
		t.Errorf("token not refreshed: %q", got.AccessToken)
	}
}
