package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 对话功能（复刻官方 grok CLI 交互）：
// 模型选择 / 思考等级（reasoning effort）/ feat 任务模式（工具调用）/ 实时流式输出。
// 走全账号池（__all__，无分组约束），内部 key 由 EnsureDefaults 自动创建。

// ChatMessage 单条会话历史。
type ChatMessage struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

// ChatRequest 对话请求。
type ChatRequest struct {
	Model   string        `json:"model"`
	Effort  string        `json:"effort"` // none|minimal|low|medium|high|xhigh|max（官方 CLI 值域）
	Feat    bool          `json:"feat"`   // feat 任务模式：开启服务端工具（web_search/x_search）
	Message string        `json:"message"`
	History []ChatMessage `json:"history,omitempty"`
}

// chatEffortToResponses 官方 CLI effort 值域 → Responses API reasoning.effort。
func chatEffortToResponses(effort string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "auto":
		return "medium", true
	case "none":
		return "minimal", true
	case "minimal":
		return "minimal", true
	case "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	case "xhigh", "max":
		return "xhigh", true
	default:
		return "medium", true
	}
}

// buildChatResponsesInput history + 当前消息 → Responses input 数组。
func buildChatResponsesInput(history []ChatMessage, message string) []map[string]any {
	var input []map[string]any
	for _, m := range history {
		role := strings.TrimSpace(m.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if role == "user" {
			input = append(input, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "input_text", "text": content}},
			})
		} else {
			input = append(input, map[string]any{
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": content}},
			})
		}
	}
	if strings.TrimSpace(message) != "" {
		input = append(input, map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": strings.TrimSpace(message)}},
		})
	}
	if len(input) == 0 {
		input = append(input, map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "你好"}},
		})
	}
	return input
}

// HandleChat 对话 SSE 接口（admin 路由 /api/v1/admin/chat）。
// 前端 EventSource/流式 fetch 消费：meta / thinking_delta / text_delta / tool_call / done / error。
func (g *GatewayService) HandleChat(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeChatError(c, "参数无效: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" && len(req.History) == 0 {
		writeChatError(c, "消息不能为空")
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultGatewayModel
	}
	effort, _ := chatEffortToResponses(req.Effort)

	// 全账号池选号（无分组约束）
	entry, err := g.picker.Pick(c.Request.Context(), AllAccountsGroupID, nil)
	if err != nil {
		writeChatError(c, err.Error())
		return
	}
	defer g.picker.Release(entry.Account.ID)

	// 构造 Responses 请求
	payload := map[string]any{
		"model":     model,
		"input":     buildChatResponsesInput(req.History, req.Message),
		"stream":    true,
		"reasoning": map[string]any{"effort": effort, "summary": "auto"},
	}
	if req.Feat {
		payload["tools"] = []map[string]any{
			{"type": "web_search"},
			{"type": "x_search"},
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		writeChatError(c, "请求序列化失败: "+err.Error())
		return
	}

	svc, err := g.store.GetService(c.Request.Context(), DefaultServiceID)
	if err != nil {
		writeChatError(c, err.Error())
		return
	}

	res, err := g.forwardUpstream(c.Request.Context(), svc.BaseURL, "/responses", http.MethodPost, bytes.NewReader(body), "application/json", "text/event-stream", entry.Account, "", true)
	if err != nil {
		g.CountRequest(false)
		writeChatError(c, "上游转发失败: "+err.Error())
		return
	}
	defer res.Body.Close()

	if res.Status >= 400 {
		g.picker.RecordFailure(entry.Account.ID, res.Status, "")
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		g.CountRequest(false)
		writeChatError(c, upstreamErrorMessage(raw, res.Status))
		return
	}

	// SSE 透传转换
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)

	// meta：模型 + 账号（脱敏）
	g.CountRequest(true)
	sendChatEvent(c, "meta", map[string]any{
		"model":   model,
		"account": maskEmail(entry.Account.Email),
	})

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" || data == "[DONE]" {
			continue
		}
		if !gjson.Valid(data) {
			continue
		}
		evtType := gjson.Get(data, "type").String()
		switch evtType {
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			sendChatEvent(c, "thinking_delta", map[string]any{"text": gjson.Get(data, "delta").String()})
		case "response.output_text.delta":
			sendChatEvent(c, "text_delta", map[string]any{"text": gjson.Get(data, "delta").String()})
		case "response.output_item.added":
			if it := gjson.Get(data, "item"); it.Exists() {
				typ := it.Get("type").String()
				if typ == "web_search_call" || typ == "x_search_call" || strings.Contains(typ, "search") {
					sendChatEvent(c, "tool_call", map[string]any{
						"id":     it.Get("id").String(),
						"name":   typ,
						"status": "start",
					})
				}
			}
		case "response.output_item.done":
			if it := gjson.Get(data, "item"); it.Exists() {
				typ := it.Get("type").String()
				if strings.Contains(typ, "search") {
					status := "done"
					// 搜索摘要：优先 query，其次 status
					summary := it.Get("search.query").String()
					if summary == "" {
						summary = it.Get("search.status").String()
					}
					sendChatEvent(c, "tool_call", map[string]any{
						"id":     it.Get("id").String(),
						"name":   typ,
						"status": status,
						"detail": summary,
					})
				}
			}
		case "response.completed", "response.done", "response.incomplete", "response.failed":
			resp := gjson.Get(data, "response")
			sendChatEvent(c, "done", map[string]any{
				"status": resp.Get("status").String(),
				"id":     resp.Get("id").String(),
			})
		}
	}
}

// maskEmail 邮箱脱敏：abc***@domain。
func maskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 1 {
		return email
	}
	return email[:1] + "***" + email[at:]
}

// sendChatEvent 写一条 SSE 事件。
func sendChatEvent(c *gin.Context, event string, data map[string]any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = c.Writer.WriteString("event: " + event + "\ndata: " + string(b) + "\n\n")
	c.Writer.Flush()
}

// writeChatError 对话错误事件。
func writeChatError(c *gin.Context, message string) {
	c.Header("Content-Type", "text/event-stream")
	c.Status(http.StatusOK)
	sendChatEvent(c, "error", map[string]any{"message": message})
	sendChatEvent(c, "done", map[string]any{"status": "failed"})
}

var _ = fmt.Sprintf
