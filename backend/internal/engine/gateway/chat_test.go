package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/umbraforge/desktop/internal/engine"
)

// 假上游：模拟 grok Responses SSE（含 web_search_call 工具事件 + thinking + 文本）
// 收到的请求体存入 capturedBody 供断言。
func chatMockUpstream(capturedBody *[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := io.ReadAll(r.Body)
		*capturedBody = body

		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_c1\",\"model\":\"grok-4.5\"}}\n\n" +
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"ws_1\",\"type\":\"web_search_call\",\"search\":{\"status\":\"in_progress\"}}}\n\n" +
				"data: {\"type\":\"response.reasoning_text.delta\",\"item_id\":\"r_1\",\"delta\":\"思考中\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":1,\"content_index\":0,\"delta\":\"PONG\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"ws_1\",\"type\":\"web_search_call\",\"search\":{\"status\":\"completed\",\"query\":\"tokyo weather\"}}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_c1\",\"status\":\"completed\"}}\n\n" +
				"data: [DONE]\n\n"))
	}
}

// parseChatSSE 解析对话 SSE 输出 → 事件序列
func parseChatSSE(body string) []map[string]any {
	var out []map[string]any
	blocks := strings.Split(body, "\n\n")
	for _, b := range blocks {
		var event, data string
		for _, line := range strings.Split(b, "\n") {
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if event == "" || data == "" {
			continue
		}
		var d map[string]any
		if json.Unmarshal([]byte(data), &d) == nil {
			d["_event"] = event
			out = append(out, d)
		}
	}
	return out
}

func TestChatStreamEventConversion(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	var capturedBody []byte
	upstream := httptest.NewServer(chatMockUpstream(&capturedBody))
	defer upstream.Close()

	st := newTestStore(t)
	svc := NewGatewayService(st, engine.NewAccountService(st))
	ctx := context.Background()
	require.NoError(t, svc.Store().EnsureDefaults(ctx))

	// grok 服务指向 mock
	svcs, _ := svc.Store().listServices(ctx)
	for i := range svcs {
		svcs[i].BaseURL = upstream.URL + "/v1"
	}
	require.NoError(t, svc.Store().save(ctx, KeyServices, svcs))

	// 账号（未分组——验证 __all__ 全账号池可用）
	accts := engine.NewAccountService(st)
	_, err := accts.UpsertFromResult(ctx, map[string]any{
		"email": "chat@x.com", "access_token": "tok-chat",
	})
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/api/v1/admin/gateway/chat", bytes.NewReader([]byte(
		`{"model":"grok-4.5","effort":"xhigh","feat":true,"message":"搜天气"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = req
	svc.HandleChat(ginCtx)

	require.Equal(t, 200, rec.Code)

	// 请求体断言：effort 映射 + feat 工具
	var sent map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &sent))
	require.Equal(t, "xhigh", sent["reasoning"].(map[string]any)["effort"])
	require.Equal(t, "web_search", sent["tools"].([]any)[0].(map[string]any)["type"])

	events := parseChatSSE(rec.Body.String())
	require.NotEmpty(t, events)

	types := map[string]int{}
	for _, e := range events {
		types[e["_event"].(string)]++
	}
	require.Equal(t, 1, types["meta"])
	require.Equal(t, 1, types["thinking_delta"])
	require.Equal(t, 1, types["text_delta"])
	require.Equal(t, 2, types["tool_call"], "工具开始+完成两个事件")
	require.Equal(t, 1, types["done"])

	// tool_call 事件内容（start + done + detail）
	var toolStart, toolDone map[string]any
	for _, e := range events {
		if e["_event"] == "tool_call" {
			if e["status"] == "start" {
				toolStart = e
			} else {
				toolDone = e
			}
		}
	}
	require.NotNil(t, toolStart)
	require.Equal(t, "web_search_call", toolStart["name"])
	require.NotNil(t, toolDone)
	require.Equal(t, "tokyo weather", toolDone["detail"])
}

func TestChatEffortMapping(t *testing.T) {
	cases := map[string]string{
		"": "medium", "none": "minimal", "minimal": "minimal", "low": "low",
		"medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh",
	}
	for in, want := range cases {
		got, ok := chatEffortToResponses(in)
		require.True(t, ok)
		require.Equal(t, want, got, "effort %q", in)
	}
}

func TestPickerAllAccountsGroup(t *testing.T) {
	st := newTestStore(t)
	seedAccounts(t, st, []engine.Account{
		{Email: "a@x.com", AccessToken: "tok-a", GroupID: "g1", GroupName: "组1"},
		{Email: "b@x.com", AccessToken: "tok-b"}, // 未分组
		{Email: "c@x.com", AccessToken: ""},      // 无 token 不可用
	})
	p := NewAccountPicker(engine.NewAccountService(st))
	ctx := context.Background()

	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		e, err := p.Pick(ctx, AllAccountsGroupID, nil)
		require.NoError(t, err, "全账号池应包含未分组账号")
		require.NotEmpty(t, e.Account.AccessToken)
		seen[e.Account.Email] = true
		p.Release(e.Account.ID)
	}
	require.Equal(t, map[string]bool{"a@x.com": true, "b@x.com": true}, seen, "无分组约束应选中含未分组账号")
}

func TestEnsureDefaultsCreatesInternalKey(t *testing.T) {
	st := newTestStore(t)
	gs := NewGatewayStore(st)
	ctx := context.Background()
	require.NoError(t, gs.EnsureDefaults(ctx))

	key, err := gs.InternalKey(ctx)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key, "sk-internal-"), "内部 key 前缀: %s", key)

	// 幂等：二次初始化不重建
	require.NoError(t, gs.EnsureDefaults(ctx))
	key2, err := gs.InternalKey(ctx)
	require.NoError(t, err)
	require.Equal(t, key, key2)

	// 内部 key 可通过网关认证（全账号池）
	found, err := gs.GetKeyBySecret(ctx, key)
	require.NoError(t, err)
	require.Equal(t, AllAccountsGroupID, found.GroupID)
}
