package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/umbraforge/desktop/internal/engine"
)

// newTestGateway 构建网关 + mock 上游 + 一个可用 key。
// 返回 (gateway, mockURL, keySecret)。
func newTestGateway(t *testing.T, upstreamHandler http.HandlerFunc) (*GatewayService, string, string) {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)

	// mock 上游（记录请求供断言）
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)

	st := newTestStore(t)
	svc := NewGatewayService(st, engine.NewAccountService(st))
	ctx := context.Background()
	require.NoError(t, svc.Store().EnsureDefaults(ctx))

	// grok 类型自定义服务指向 mock（走公开 API，同时验证 grok 头注入）
	grokSvc, err := svc.Store().CreateService(ctx, "mock-grok", "grok", upstream.URL+"/v1", true)
	require.NoError(t, err)
	_, err = svc.Store().UpdateGroup(ctx, DefaultGroupID, map[string]any{"service_id": grokSvc.ID})
	require.NoError(t, err)

	// 账号入默认分组
	accts := engine.NewAccountService(st)
	_, err = accts.UpsertFromResult(ctx, map[string]any{
		"email":        "gw-test@x.com",
		"access_token": "tok-test-123",
		"group_id":     DefaultGroupID,
		"group_name":   "默认分组",
	})
	require.NoError(t, err)

	// API key
	k, err := svc.Store().CreateKey(ctx, "测试", DefaultGroupID)
	require.NoError(t, err)

	return svc, upstream.URL, k.KeyFull
}

func gatewayDo(t *testing.T, svc *GatewayService, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	svc.routes().ServeHTTP(rec, req)
	return rec
}

// ---------- OpenAI /v1/responses ----------

const responsesOK = `{"id":"resp_x","object":"response","created_at":1786000000,"status":"completed","model":"grok-4.5","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"PONG","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`

func TestGatewayOpenAIResponsesNonStream(t *testing.T) {
	var sawHeaders atomic.Bool
	upstream := func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		// grok CLI 指纹头断言
		require.Equal(t, "xai-grok-cli", r.Header.Get("X-XAI-Token-Auth"))
		require.NotEmpty(t, r.Header.Get("x-grok-client-version"))
		require.Equal(t, "grok-shell", r.Header.Get("x-grok-client-identifier"))
		require.Equal(t, "authenticate-response", r.Header.Get("x-authenticateresponse"))
		require.Contains(t, r.Header.Get("User-Agent"), "xai-grok-workspace/")
		require.Equal(t, "Bearer tok-test-123", r.Header.Get("Authorization"))
		sawHeaders.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesOK))
	}
	svc, _, key := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/responses", key, `{"model":"grok-4.5","input":"ping"}`)
	require.Equal(t, 200, rec.Code)
	require.True(t, sawHeaders.Load(), "上游必须收到 grok CLI 身份头")
	require.Contains(t, rec.Body.String(), "PONG")
}

func TestGatewayOpenAIResponsesStream(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_x\",\"model\":\"grok-4.5\"}}\n\n" +
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PONG\",\"annotations\":[]}]}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"PONG\"}\n\n" +
				"data: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"text\":\"PONG\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PONG\"}]}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"model\":\"grok-4.5\",\"status\":\"completed\"}}\n\n" +
				"data: [DONE]\n\n"))
	}
	svc, _, key := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/responses", key, `{"model":"grok-4.5","input":"ping","stream":true}`)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "response.created")
	require.Contains(t, rec.Body.String(), "PONG")
	require.Contains(t, rec.Body.String(), "response.completed")
}

// ---------- OpenAI /v1/chat/completions ----------

func TestGatewayChatCompletionsDefaultModel(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		require.Equal(t, "grok-4.5", body["model"], "缺省模型必须兜底 grok-4.5")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cc_x","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"PONG"},"finish_reason":"stop"}]}`))
	}
	svc, _, key := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/chat/completions", key, `{"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "PONG")
}

// ---------- 认证 ----------

func TestGatewayAuthRejectsBadKey(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("无效 key 不应到达上游")
	}
	svc, _, _ := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/responses", "sk-wrong", `{}`)
	require.Equal(t, 401, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid API key")

	rec2 := gatewayDo(t, svc, "POST", "/v1/responses", "", `{}`)
	require.Equal(t, 401, rec2.Code)
}

// ---------- Claude /v1/messages ----------

func TestGatewayMessagesNonStream(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path, "Claude 请求应转为 Responses 上游")
		require.NotEmpty(t, r.Header.Get("x-grok-client-version"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesOK))
	}
	svc, _, key := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/messages", key,
		`{"model":"grok-4.5","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "PONG", "Anthropic 格式响应应包含文本")
	require.Contains(t, rec.Body.String(), "stop_reason")
	require.Contains(t, rec.Body.String(), "text")
}

func TestGatewayMessagesStream(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_x\",\"model\":\"grok-4.5\"}}\n\n" +
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PONG\",\"annotations\":[]}]}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"PONG\"}\n\n" +
				"data: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"text\":\"PONG\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PONG\"}]}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"model\":\"grok-4.5\",\"status\":\"completed\"}}\n\n" +
				"data: [DONE]\n\n"))
	}
	svc, _, key := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/messages", key,
		`{"model":"grok-4.5","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "event: message_start")
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, "event: message_delta")
	require.Contains(t, body, "event: message_stop")
}

// ---------- 上游错误语义 ----------

func TestGatewayForbiddenMarksAccount(t *testing.T) {
	var calls atomic.Int32
	upstream := func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"message":"access denied"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesOK))
	}
	svc, _, key := newTestGateway(t, upstream)

	rec := gatewayDo(t, svc, "POST", "/v1/responses", key, `{"model":"grok-4.5","input":"hi"}`)
	require.Equal(t, 403, rec.Code)

	// 账号被永久封禁 → 后续请求 503 无可用账号
	rec2 := gatewayDo(t, svc, "POST", "/v1/responses", key, `{"model":"grok-4.5","input":"hi"}`)
	require.Equal(t, 503, rec2.Code)
	require.Contains(t, rec2.Body.String(), "无可用账号")
}

func TestGatewayNoAccountInGroup(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("无账号不应到达上游")
	}
	svc, _, key := newTestGateway(t, upstream)
	ctx := context.Background()
	accts := engine.NewAccountService(svc.Store().st)
	// 把账号移出分组（清空分组 = 移出）
	all, err := accts.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	_, err = accts.MoveAccounts(ctx, []string{all[0].ID}, "", "")
	require.NoError(t, err)

	rec := gatewayDo(t, svc, "POST", "/v1/responses", key, `{"model":"grok-4.5","input":"hi"}`)
	require.Equal(t, 503, rec.Code)
	require.Contains(t, rec.Body.String(), "无可用账号")
}

func TestGatewayModelsEndpoint(t *testing.T) {
	// models = 上游动态目录 ∪ 内置官方清单
	upstream := func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("x-grok-client-version"), "models 请求应带 grok CLI 身份头")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"grok-4.5","object":"model","owned_by":"xAI","name":"Grok 4.5","context_window":500000,"supports_reasoning_effort":true,"reasoning_efforts":[{"id":"high","label":"High Effort"},{"id":"low","label":"Low Effort"}]},
			{"id":"grok-4.5-thinking","object":"model","owned_by":"xAI","name":"Grok 4.5 Thinking"}
		]}`))
	}
	svc, _, key := newTestGateway(t, upstream)
	rec := gatewayDo(t, svc, "GET", "/v1/models", key, "")
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "grok-4.5")
	require.Contains(t, rec.Body.String(), "grok-4.5-thinking")
	require.Contains(t, rec.Body.String(), "context_window", "应透传官方模型元数据（含 reasoning_efforts）")
	// 内置官方清单补齐：上游没有的模型也应列出（sub2api 同款行为）
	require.Contains(t, rec.Body.String(), "grok-4.3", "内置官方清单应补齐")
	require.Contains(t, rec.Body.String(), "grok-imagine-video-1.5", "内置官方清单应补齐")
	require.Contains(t, rec.Body.String(), "Grok Build 0.1", "清单 DisplayName 应透传")
}

// ---------- 代理出口 ----------

func TestGatewayProxyUsed(t *testing.T) {
	// 代理地址不可达时转发应失败并走重试，最终返回 502（验证代理被尝试）
	upstream := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesOK))
	}
	svc, _, key := newTestGateway(t, upstream)
	ctx := context.Background()
	_, err := svc.Store().UpdateGroup(ctx, DefaultGroupID, map[string]any{
		"proxy_ids": []any{"http://127.0.0.1:1"},
	})
	require.NoError(t, err)
	rec := gatewayDo(t, svc, "POST", "/v1/responses", key, `{"model":"grok-4.5","input":"hi"}`)
	require.Equal(t, 502, rec.Code, "不可达代理应导致转发失败（分组代理被使用）")
	require.Contains(t, rec.Body.String(), "上游转发失败")
}

var _ = fmt.Sprintf
