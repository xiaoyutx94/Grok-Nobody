package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/umbraforge/desktop/internal/engine/gateway"
)

// registerGatewayRoutes 网关管理 API（分组 / Key / 服务 / 配置 / 账号移动）。
func (a *API) registerGatewayRoutes(admin *gin.RouterGroup) {
	gw := admin.Group("/gateway")

	// ---------- 状态与配置 ----------
	gw.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, a.Gateway.Status())
	})
	// 可访问地址：监听配置 + 本机 LAN IP（内外网连接用）
	gw.GET("/addresses", func(c *gin.Context) {
		cfg, err := a.Gateway.Store().GetConfig(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		host := cfg.ListenHost
		if host == "" {
			host = gateway.DefaultListenHost
		}
		lan := a.Gateway.LocalAddresses()
		c.JSON(http.StatusOK, gin.H{
			"listen_host": host,
			"lan":         lan,
			"local":       fmt.Sprintf("127.0.0.1:%d", cfg.Port),
			"port":        cfg.Port,
		})
	})
	gw.GET("/config", func(c *gin.Context) {
		cfg, err := a.Gateway.Store().GetConfig(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, cfg)
	})
	gw.PUT("/config", func(c *gin.Context) {
		var cfg gateway.GatewayConfig
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		if err := a.Gateway.ApplyConfig(c.Request.Context(), cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, a.Gateway.Status())
	})

	// ---------- 分组 ----------
	gw.GET("/groups", func(c *gin.Context) {
		list, err := a.Gateway.Store().ListGroups(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 附加每组账号统计
		stats := make(map[string]gateway.Snapshot, len(list))
		for _, g := range list {
			snap, err := a.Gateway.Picker().Snapshot(c.Request.Context(), g.ID)
			if err == nil {
				stats[g.ID] = snap
			}
		}
		c.JSON(http.StatusOK, gin.H{"groups": list, "stats": stats})
	})
	gw.POST("/groups", func(c *gin.Context) {
		var body struct {
			Name      string   `json:"name"`
			Desc      string   `json:"desc"`
			ServiceID string   `json:"service_id"`
			ProxyIDs  []string `json:"proxy_ids"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		g, err := a.Gateway.Store().CreateGroup(c.Request.Context(), body.Name, body.Desc, body.ServiceID, body.ProxyIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, g)
	})
	gw.PUT("/groups/:id", func(c *gin.Context) {
		var patch map[string]any
		if err := c.ShouldBindJSON(&patch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		g, err := a.Gateway.Store().UpdateGroup(c.Request.Context(), c.Param("id"), patch)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, g)
	})
	gw.DELETE("/groups/:id", func(c *gin.Context) {
		if err := a.Gateway.Store().DeleteGroup(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ---------- API Key ----------
	gw.GET("/keys", func(c *gin.Context) {
		list, err := a.Gateway.Store().ListKeys(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"keys": list})
	})
	gw.POST("/keys", func(c *gin.Context) {
		var body struct {
			Name    string `json:"name"`
			GroupID string `json:"group_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		k, err := a.Gateway.Store().CreateKey(c.Request.Context(), body.Name, body.GroupID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, k)
	})
	gw.PUT("/keys/:id", func(c *gin.Context) {
		var body struct {
			Status string `json:"status"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		if body.Status != gateway.KeyStatusActive && body.Status != gateway.KeyStatusDisabled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status 仅支持 active|disabled"})
			return
		}
		k, err := a.Gateway.Store().SetKeyStatus(c.Request.Context(), c.Param("id"), body.Status)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, k)
	})
	gw.DELETE("/keys/:id", func(c *gin.Context) {
		if err := a.Gateway.Store().DeleteKey(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// 复制完整密钥（列表默认脱敏，仅按需取回）
	gw.POST("/keys/:id/reveal", func(c *gin.Context) {
		k, err := a.Gateway.Store().GetKeyFull(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"key": k.KeyFull})
	})

	// ---------- 服务 ----------
	gw.GET("/services", func(c *gin.Context) {
		list, err := a.Gateway.Store().ListServices(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"services": list})
	})
	gw.POST("/services", func(c *gin.Context) {
		var body struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			BaseURL string `json:"base_url"`
			Enabled bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		svc, err := a.Gateway.Store().CreateService(c.Request.Context(), body.Name, body.Type, body.BaseURL, body.Enabled)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, svc)
	})
	gw.PUT("/services/:id", func(c *gin.Context) {
		var patch map[string]any
		if err := c.ShouldBindJSON(&patch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		svc, err := a.Gateway.Store().UpdateService(c.Request.Context(), c.Param("id"), patch)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, svc)
	})
	gw.DELETE("/services/:id", func(c *gin.Context) {
		if err := a.Gateway.Store().DeleteService(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ---------- 账号 ----------
	gw.GET("/accounts", func(c *gin.Context) {
		// 按分组列出账号（供移动弹窗选择）
		accts, err := a.Accounts.List(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		type acctView struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			GroupID   string `json:"group_id"`
			GroupName string `json:"group_name"`
			HasToken  bool   `json:"has_token"`
		}
		out := make([]acctView, 0, len(accts))
		for _, a2 := range accts {
			out = append(out, acctView{
				ID: a2.ID, Email: a2.Email, GroupID: a2.GroupID, GroupName: a2.GroupName,
				HasToken: strings.TrimSpace(a2.AccessToken) != "",
			})
		}
		c.JSON(http.StatusOK, gin.H{"accounts": out})
	})
	gw.POST("/accounts/move", func(c *gin.Context) {
		var body struct {
			IDs       []string `json:"ids"`
			GroupID   string   `json:"group_id"`
			GroupName string   `json:"group_name"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		if len(body.IDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ids 不能为空"})
			return
		}
		// 清空分组时两个都传空
		if body.GroupID != "" {
			g, err := a.Gateway.Store().GetGroup(c.Request.Context(), body.GroupID)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			body.GroupName = g.Name
		}
		moved, err := a.Accounts.MoveAccounts(c.Request.Context(), body.IDs, body.GroupID, body.GroupName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"moved": moved})
	})

	// ---------- 连通性测试 ----------
	gw.POST("/test", func(c *gin.Context) {
		var body struct {
			GroupID string `json:"group_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
			return
		}
		group, err := a.Gateway.Store().GetGroup(c.Request.Context(), body.GroupID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		svc, err := a.Gateway.Store().GetService(c.Request.Context(), group.ServiceID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		entry, err := a.Gateway.Picker().Pick(c.Request.Context(), group.ID, group.ProxyIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分组无可用账号: " + err.Error()})
			return
		}
		defer a.Gateway.Picker().Release(entry.Account.ID)
		c.JSON(http.StatusOK, gin.H{
			"service": svc.Name,
			"account": entry.Account.Email,
			"proxy":   entry.Proxy,
		})
	})

	_ = context.Background
}
