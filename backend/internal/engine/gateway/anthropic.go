package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/umbraforge/desktop/internal/pkg/apicompat"
)

// handleMessages Claude 协议端点：Anthropic Messages → Responses 上游 → 转回 Anthropic。
// 复用 sub2api apicompat 包（整包移植，测试全绿），主流客户端（Claude Code/CLI）可用。
func (g *GatewayService) handleMessages() gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes))
		if err != nil {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "读取请求体失败: "+err.Error())
			return
		}

		var anthReq apicompat.AnthropicRequest
		if err := json.Unmarshal(body, &anthReq); err != nil {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 Anthropic Messages 格式: "+err.Error())
			return
		}
		origModel := strings.TrimSpace(anthReq.Model)
		if origModel == "" {
			origModel = DefaultGatewayModel
			anthReq.Model = origModel
		}

		responsesReq, err := apicompat.AnthropicToResponses(&anthReq)
		if err != nil {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "请求转换失败: "+err.Error())
			return
		}
		outBody, err := json.Marshal(responsesReq)
		if err != nil {
			writeAnthropicError(c, http.StatusInternalServerError, "api_error", "请求序列化失败: "+err.Error())
			return
		}

		group, svc, entry, err := g.resolveTarget(c, sessionIDFromRequest(c))
		if err != nil {
			writeAnthropicError(c, http.StatusServiceUnavailable, "overloaded_error", err.Error())
			return
		}
		defer g.picker.Release(entry.Account.ID)

		stream := anthReq.Stream
		accept := "application/json"
		if stream {
			accept = "text/event-stream"
		}
		res, err := g.forwardWithRetry(c.Request.Context(), c, group, svc, entry, "/responses", outBody, accept, svc.Type == "grok")
		if err != nil {
			g.CountRequest(false)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "上游转发失败: "+err.Error())
			return
		}
		defer res.Body.Close()

		if res.Status >= 400 {
			if secs, forbidden := classifyUpstream(res.Status, ""); secs > 0 || forbidden {
				g.picker.RecordFailure(res.AccountID, res.Status, "")
			}
			// 透传上游错误（转 Anthropic 错误格式）
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			writeAnthropicError(c, res.Status, "api_error", upstreamErrorMessage(raw, res.Status))
			g.CountRequest(false)
			return
		}

		if stream {
			g.streamAnthropic(c, res, origModel)
		} else {
			g.bufferAnthropic(c, res, origModel)
		}
		g.CountRequest(true)
	}
}

// bufferAnthropic 非流式：上游 Responses JSON → Anthropic 响应。
func (g *GatewayService) bufferAnthropic(c *gin.Context, res *ForwardResult, origModel string) {
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "读取上游响应失败: "+err.Error())
		return
	}
	var resp apicompat.ResponsesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		// 非 JSON 响应（如网关 HTML 错误页）原样透传
		copyResponseHeaders(c, res.Header)
		c.Status(res.Status)
		_, _ = c.Writer.Write(raw)
		return
	}
	anth := apicompat.ResponsesToAnthropic(&resp, origModel)
	out, err := json.Marshal(anth)
	if err != nil {
		writeAnthropicError(c, http.StatusInternalServerError, "api_error", "响应序列化失败: "+err.Error())
		return
	}
	c.Header("Content-Type", "application/json")
	c.Status(res.Status)
	_, _ = c.Writer.Write(out)
}

// streamAnthropic 流式：上游 Responses SSE → Anthropic SSE 事件流。
func (g *GatewayService) streamAnthropic(c *gin.Context, res *ForwardResult, origModel string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(res.Status)

	state := apicompat.NewResponsesEventToAnthropicState()
	state.Model = origModel

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
		var evt apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		for _, anth := range apicompat.ResponsesEventToAnthropicEvents(&evt, state) {
			sse, err := apicompat.ResponsesAnthropicEventToSSE(anth)
			if err != nil {
				continue
			}
			_, _ = c.Writer.WriteString(sse)
			c.Writer.Flush()
		}
	}
	for _, anth := range apicompat.FinalizeResponsesAnthropicStream(state) {
		sse, err := apicompat.ResponsesAnthropicEventToSSE(anth)
		if err == nil {
			_, _ = c.Writer.WriteString(sse)
			c.Writer.Flush()
		}
	}
}

// writeAnthropicError 输出 Anthropic 兼容错误体。
func writeAnthropicError(c *gin.Context, status int, errType, message string) {
	c.Header("Content-Type", "application/json")
	c.Status(status)
	_ = json.NewEncoder(c.Writer).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
}

// upstreamErrorMessage 从上游错误体提取可读 message。
func upstreamErrorMessage(raw []byte, status int) string {
	if len(raw) == 0 {
		return http.StatusText(status)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if errObj, ok := m["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := m["message"].(string); ok && msg != "" {
		return msg
	}
	return strings.TrimSpace(string(raw))
}

var _ = bytes.NewReader // 保留 bytes 依赖（与 http.go 共用语义）
