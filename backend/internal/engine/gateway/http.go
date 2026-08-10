package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const maxBodyBytes = 64 << 20 // 64MB

// DefaultGatewayModel 请求体缺省模型（用户要求 grok-4.5）。
const DefaultGatewayModel = "grok-4.5"

// routes 构建网关 HTTP 路由（独立 gin 实例，仅 127.0.0.1）。
func (g *GatewayService) routes() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.POST("/v1/responses", g.auth(), g.handleForward("responses"))
	r.POST("/v1/chat/completions", g.auth(), g.handleForward("chat"))
	r.POST("/v1/messages", g.auth(), g.handleMessages())
	r.GET("/v1/models", g.auth(), g.handleModels())
	return r
}

// resolveTarget 按 Key 分组解析：分组 → 服务 → 可用账号。
// 调用方负责 picker.Release。
// groupID 为 AllAccountsGroupID 时跳过分组查询（全账号池 + 默认 grok 反代服务）。
func (g *GatewayService) resolveTarget(c *gin.Context) (GatewayGroup, UpstreamService, AccountEntry, error) {
	groupID := c.GetString("gw_group_id")
	ctx := c.Request.Context()
	group := GatewayGroup{ID: groupID, ServiceID: DefaultServiceID}
	if groupID != AllAccountsGroupID {
		var err error
		group, err = g.store.GetGroup(ctx, groupID)
		if err != nil {
			return GatewayGroup{}, UpstreamService{}, AccountEntry{}, err
		}
	}
	svc, err := g.store.GetService(ctx, group.ServiceID)
	if err != nil {
		return GatewayGroup{}, UpstreamService{}, AccountEntry{}, err
	}
	if !svc.Enabled {
		return GatewayGroup{}, UpstreamService{}, AccountEntry{}, fmt.Errorf("服务已禁用: %s", svc.Name)
	}
	entry, err := g.picker.Pick(ctx, group.ID, group.ProxyIDs)
	if err != nil {
		return GatewayGroup{}, UpstreamService{}, AccountEntry{}, err
	}
	return group, svc, entry, nil
}

// requestStream 从请求体判断是否流式。
func requestStream(body []byte) bool {
	return gjson.GetBytes(body, "stream").Bool()
}

// ensureModel 请求体缺省模型兜底（grok-4.5）。
func ensureModel(body []byte) []byte {
	if gjson.GetBytes(body, "model").String() != "" {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	m["model"] = DefaultGatewayModel
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// upstreamPath 原路径 /v1/xxx → 上游相对路径（BaseURL 已含 /v1）。
func upstreamPath(urlPath string) string {
	return strings.TrimPrefix(urlPath, "/v1")
}

// handleForward OpenAI 协议统一处理（responses / chat completions）。
func (g *GatewayService) handleForward(proto string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes))
		if err != nil {
			writeOpenAIError(c, http.StatusBadRequest, "读取请求体失败: "+err.Error())
			return
		}
		body = ensureModel(body)
		stream := requestStream(body)

		group, svc, entry, err := g.resolveTarget(c)
		if err != nil {
			writeOpenAIError(c, http.StatusServiceUnavailable, err.Error())
			return
		}
		defer g.picker.Release(entry.Account.ID)

		path := upstreamPath(c.Request.URL.Path)
		accept := "application/json"
		if stream {
			accept = "text/event-stream"
		}

		res, err := g.forwardWithRetry(ctx, c, group, svc, entry, path, body, accept, svc.Type == "grok")
		if err != nil {
			g.CountRequest(false)
			writeOpenAIError(c, http.StatusBadGateway, "上游转发失败: "+err.Error())
			return
		}
		defer res.Body.Close()

		// 上游错误 → 冷却/封禁记录
		if res.Status >= 400 {
			if secs, forbidden := classifyUpstream(res.Status, ""); secs > 0 || forbidden {
				g.picker.RecordFailure(res.AccountID, res.Status, "")
			}
		}

		// 透传响应
		copyResponseHeaders(c, res.Header)
		c.Status(res.Status)
		if isSSEStream(res.Header) {
			flushCopy(c, res.Body)
		} else {
			out, _ := io.ReadAll(res.Body)
			_, _ = c.Writer.Write(out)
		}
		g.CountRequest(res.Status < 400)
	}
}

// forwardWithRetry 转发并处理重试：网络类错误换账号重试 1 次；
// 429/403 不重试（冷却/封禁语义）。
func (g *GatewayService) forwardWithRetry(ctx interface{ Done() <-chan struct{}; Err() error }, c *gin.Context, group GatewayGroup, svc UpstreamService, entry AccountEntry, path string, body []byte, accept string, grokService bool) (*ForwardResult, error) {
	current := entry
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		res, err := g.forwardUpstream(c.Request.Context(), svc.BaseURL, path, http.MethodPost, bytes.NewReader(body), "application/json", accept, current.Account, current.Proxy, grokService)
		if err == nil {
			return res, nil
		}
		lastErr = err
		g.picker.RecordFailure(current.Account.ID, 0, err.Error())
		g.picker.Release(current.Account.ID)
		if attempt == 1 {
			break
		}
		next, err2 := g.picker.Pick(c.Request.Context(), group.ID, group.ProxyIDs)
		if err2 != nil {
			break
		}
		current = next
	}
	return nil, lastErr
}

// copyResponseHeaders 复制上游响应头（跳过 hop-by-hop）。
func copyResponseHeaders(c *gin.Context, h http.Header) {
	for k, vs := range h {
		switch strings.ToLower(k) {
		case "content-length", "connection", "transfer-encoding", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "upgrade":
			continue
		}
		for _, v := range vs {
			c.Header(k, v)
		}
	}
}

// flushCopy 流式透传 + Flush。
func flushCopy(c *gin.Context, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = c.Writer.Write(buf[:n])
			c.Writer.Flush()
		}
		if err != nil {
			break
		}
	}
}

// handleModels 返回分组可用模型列表。
func (g *GatewayService) handleModels() gin.HandlerFunc {
	return func(c *gin.Context) {
		models := []map[string]any{
			{"id": "grok-4.5", "object": "model", "owned_by": "xai"},
			{"id": "grok-4.5-thinking", "object": "model", "owned_by": "xai"},
			{"id": "grok-4", "object": "model", "owned_by": "xai"},
			{"id": "grok-4-fast", "object": "model", "owned_by": "xai"},
			{"id": "grok-3.5", "object": "model", "owned_by": "xai"},
			{"id": "grok-3", "object": "model", "owned_by": "xai"},
			{"id": "grok-2", "object": "model", "owned_by": "xai"},
		}
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
	}
}

var _ = errors.Is // 保留 import 兼容（errors 用于将来错误分类扩展）
