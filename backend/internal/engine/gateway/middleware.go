package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// auth 网关 API Key 认证中间件：Authorization: Bearer sk-xxx。
// 通过后 c.Set("gw_group_id", key.GroupID)。失败返回 OpenAI 风格 401。
func (g *GatewayService) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		secret := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if !strings.HasPrefix(secret, "sk-") {
			writeOpenAIError(c, http.StatusUnauthorized, "Invalid API key")
			c.Abort()
			return
		}
		key, err := g.store.GetKeyBySecret(c.Request.Context(), secret)
		if err != nil {
			writeOpenAIError(c, http.StatusUnauthorized, "Invalid API key")
			c.Abort()
			return
		}
		c.Set("gw_group_id", key.GroupID)
		c.Next()
	}
}

// writeOpenAIError 输出 OpenAI 兼容错误体。
func writeOpenAIError(c *gin.Context, status int, message string) {
	c.Header("Content-Type", "application/json")
	c.Status(status)
	_ = json.NewEncoder(c.Writer).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    http.StatusText(status),
		},
	})
}
