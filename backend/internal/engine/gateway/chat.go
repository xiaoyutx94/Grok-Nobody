package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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
	Feat    bool          `json:"feat"`   // feat 任务模式：开启服务端工具（web_search/x_search）+ web_fetch
	Message string        `json:"message"`
	History []ChatMessage `json:"history,omitempty"`
	// Images 粘贴的图片（data URL，如 data:image/png;base64,...）
	Images []string `json:"images,omitempty"`
}

// webFetchTool web_fetch 客户端工具定义（feat 模式下由模型调用，后端执行）。
var webFetchTool = map[string]any{
	"type": "function",
	"name": "web_fetch",
	"description": "Fetch the content of a web page or URL and return its text. " +
		"Use this to read articles, docs, or API responses. Max 2MB.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The full URL to fetch (http/https)",
			},
		},
		"required": []string{"url"},
	},
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

// chatFunctionCall 一次 function_call 调用。
type chatFunctionCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"arguments"`
}

// HandleChat 对话 SSE 接口（admin 路由 /api/v1/admin/chat）。
// 前端 EventSource/流式 fetch 消费：meta / thinking_delta / text_delta / tool_call / tool_result / done / error。
// feat 模式：web_search/x_search（服务端工具，上游执行）+ web_fetch（客户端工具，后端执行循环）。
func (g *GatewayService) HandleChat(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeChatError(c, "参数无效: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" && len(req.History) == 0 && len(req.Images) == 0 {
		writeChatError(c, "消息不能为空")
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultGatewayModel
	}
	effort, _ := chatEffortToResponses(req.Effort)

	// 全账号池选号（无分组约束）；整轮对话复用同一账号
	entry, err := g.picker.Pick(c.Request.Context(), AllAccountsGroupID, nil)
	if err != nil {
		writeChatError(c, err.Error())
		return
	}
	defer g.picker.Release(entry.Account.ID)

	svc, err := g.store.GetService(c.Request.Context(), DefaultServiceID)
	if err != nil {
		writeChatError(c, err.Error())
		return
	}

	// 构造 input（历史 + 消息 + 图片）
	input := buildChatResponsesInput(req.History, req.Message)
	if len(req.Images) > 0 {
		content, ok := input[len(input)-1]["content"].([]map[string]any)
		if ok {
			for _, img := range req.Images {
				if strings.HasPrefix(img, "data:image/") {
					content = append(content, map[string]any{"type": "input_image", "image_url": img})
				}
			}
			input[len(input)-1]["content"] = content
		}
	}

	var tools []map[string]any
	if req.Feat {
		tools = []map[string]any{
			{"type": "web_search"},
			{"type": "x_search"},
			webFetchTool,
		}
	}

	// SSE 头 + meta
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
	g.CountRequest(true)
	sendChatEvent(c, "meta", map[string]any{
		"model":   model,
		"account": maskEmail(entry.Account.Email),
	})

	// 工具循环：模型 function_call → 后端执行 → 结果回传 → 再请求（最多 3 轮）
	for turn := 0; turn < 3; turn++ {
		payload := map[string]any{
			"model":     model,
			"input":     input,
			"stream":    true,
			"reasoning": map[string]any{"effort": effort, "summary": "auto"},
		}
		if len(tools) > 0 {
			payload["tools"] = tools
		}
		body, err := json.Marshal(payload)
		if err != nil {
			writeChatError(c, "请求序列化失败: "+err.Error())
			return
		}

		res, err := g.forwardUpstream(c.Request.Context(), svc.BaseURL, "/responses", http.MethodPost, bytes.NewReader(body), "application/json", "text/event-stream", entry.Account, "", true)
		if err != nil {
			g.CountRequest(false)
			writeChatError(c, "上游转发失败: "+err.Error())
			return
		}
		if res.Status >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			g.picker.RecordFailure(entry.Account.ID, res.Status, "")
			g.CountRequest(false)
			writeChatError(c, upstreamErrorMessage(raw, res.Status))
			return
		}

		var fc *chatFunctionCall
		var imgB64 string // 累积中的图片 base64（response.output_image.delta）
		sendImage := func(b64 string) {
			imgB64 = ""
			if b64 == "" {
				return
			}
			mime := "image/png"
			if strings.HasPrefix(b64, "/9j/") {
				mime = "image/jpeg"
			}
			sendChatEvent(c, "image_delta", map[string]any{"data_url": "data:" + mime + ";base64," + b64})
		}
		handleImagePart := func(part gjson.Result) bool {
			// part: {"type":"output_image","image_url":...} 或 {"type":"output_image","b64_json":...}
			if u := part.Get("image_url").String(); u != "" {
				sendChatEvent(c, "image_delta", map[string]any{"data_url": u})
				return true
			}
			if b := part.Get("b64_json").String(); b != "" {
				sendImage(b)
				return true
			}
			return false
		}
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
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
			case "response.output_image.delta":
				imgB64 += gjson.Get(data, "delta").String()
			case "response.output_image.done":
				// 完整图片：优先 image_url / b64_json，其次累积的 delta b64
				if it := gjson.Get(data, "part"); it.Exists() && handleImagePart(it) {
					// 已发送
				} else if imgB64 != "" {
					sendImage(imgB64)
				}
			case "response.output_item.added":
				if it := gjson.Get(data, "item"); it.Exists() {
					typ := it.Get("type").String()
					switch {
					case strings.Contains(typ, "search"):
						sendChatEvent(c, "tool_call", map[string]any{
							"id": it.Get("id").String(), "name": typ, "status": "start",
						})
					case typ == "function_call":
						fc = &chatFunctionCall{
							ID:   it.Get("call_id").String(),
							Name: it.Get("name").String(),
						}
						sendChatEvent(c, "tool_call", map[string]any{
							"id": it.Get("call_id").String(), "name": fc.Name, "status": "start",
						})
					}
				}
			case "response.function_call_arguments.delta":
				if fc != nil {
					fc.Args += gjson.Get(data, "delta").String()
				}
			case "response.content_part.done":
				if part := gjson.Get(data, "part"); part.Exists() && part.Get("type").String() == "output_image" {
					handleImagePart(part)
				}
			case "response.output_item.done":
				if it := gjson.Get(data, "item"); it.Exists() {
					typ := it.Get("type").String()
					// 图片输出：item.content[] 里的 output_image part
					if typ == "message" {
						for _, part := range it.Get("content").Array() {
							if part.Get("type").String() == "output_image" {
								handleImagePart(part)
							}
						}
					}
					switch {
					case strings.Contains(typ, "search"):
						summary := it.Get("search.query").String()
						if summary == "" {
							summary = it.Get("search.status").String()
						}
						sendChatEvent(c, "tool_call", map[string]any{
							"id": it.Get("id").String(), "name": typ, "status": "done", "detail": summary,
						})
					case typ == "function_call":
						if fc != nil {
							if fc.Args == "" {
								fc.Args = it.Get("arguments").Raw
							}
							sendChatEvent(c, "tool_call", map[string]any{
								"id": fc.ID, "name": fc.Name, "status": "done",
								"detail": clipText(fc.Args, 120),
							})
						}
					}
				}
			case "response.completed", "response.done", "response.incomplete", "response.failed":
				resp := gjson.Get(data, "response")
				if fc != nil {
					// 有未完成的工具调用：跳过本轮完成事件，进入执行循环
					_ = resp
					continue
				}
				sendChatEvent(c, "done", map[string]any{
					"status": resp.Get("status").String(),
					"id":     resp.Get("id").String(),
				})
			}
		}
		res.Body.Close()

		if fc == nil {
			return // 无工具调用，正常结束
		}
		// 执行客户端工具（web_fetch）→ 结果回传 → 下一轮
		output := g.executeChatTool(fc)
		sendChatEvent(c, "tool_result", map[string]any{
			"name": fc.Name, "id": fc.ID,
			"output": clipText(output, 400),
		})
		input = append(input,
			map[string]any{"type": "function_call", "call_id": fc.ID, "name": fc.Name, "arguments": fc.Args},
			map[string]any{"type": "function_call_output", "call_id": fc.ID, "output": output},
		)
	}
	// 达到最大轮数
	sendChatEvent(c, "done", map[string]any{"status": "completed", "truncated": true})
}

// executeChatTool 执行客户端工具调用（当前支持 web_fetch）。
func (g *GatewayService) executeChatTool(fc *chatFunctionCall) string {
	if fc.Name != "web_fetch" {
		return "工具不可用: " + fc.Name
	}
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(fc.Args), &args); err != nil {
		return "参数解析失败: " + err.Error()
	}
	url := strings.TrimSpace(args.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "仅支持 http/https URL"
	}
	// 基本 SSRF 防护：拒绝本机/内网地址
	if blocked, reason := blockPrivateURL(url); blocked {
		return "拒绝访问: " + reason
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "抓取失败: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "读取失败: " + err.Error()
	}
	if len(raw) == 0 {
		return "（空响应）"
	}
	return clipText(string(raw), 8000)
}

// clipText 截断文本（保留开头 + 截断标记）。
func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[已截断]"
}

// blockPrivateURL SSRF 防护：拒绝本机/链路本地/私有网段地址。
// 环境变量 GATEWAY_CHAT_ALLOW_PRIVATE=1 时放行（仅测试用）。
func blockPrivateURL(raw string) (bool, string) {
	if os.Getenv("GATEWAY_CHAT_ALLOW_PRIVATE") == "1" {
		return false, ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return true, "URL 无效"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return true, "仅支持 http/https"
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true, "本机地址"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// 域名：解析后检查（只查首个 A 记录，简单防护）
		ips, err := net.LookupIP(host)
		if err != nil {
			return false, ""
		}
		ip = ips[0]
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true, "内网/特殊地址"
	}
	return false, ""
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
