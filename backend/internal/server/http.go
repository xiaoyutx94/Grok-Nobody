package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/umbraforge/desktop/internal/engine"
	"github.com/umbraforge/desktop/internal/pkg/cfemail"
	"github.com/umbraforge/desktop/internal/pkg/xai"
	"github.com/umbraforge/desktop/internal/plugins"
	"github.com/umbraforge/desktop/internal/procutil"
)

type API struct {
	Engine   *engine.RegisterEngine
	Edu      *engine.EduService
	Plugins  *plugins.Center
	Accounts *engine.AccountService
	Warp     *engine.WarpService
	Static   fs.FS
}

func (a *API) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "app": "UmbraForge", "product": "影铸"})
	})

	admin := r.Group("/api/v1/admin")

	// 关于弹窗的外链跳转：webview 不处理 target=_blank，由后端调系统浏览器。
	// 白名单：仅放行本程序的官方仓库与 QQ 群。
	admin.POST("/misc/open-external", func(c *gin.Context) {
		var body struct {
			URL string `json:"url"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": "bad json"})
			return
		}
		u, err := url.Parse(strings.TrimSpace(body.URL))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			c.JSON(400, gin.H{"error": "仅支持 http/https 链接"})
			return
		}
		host := strings.ToLower(u.Hostname())
		if host != "github.com" && host != "qm.qq.com" {
			c.JSON(400, gin.H{"error": "域名不在白名单"})
			return
		}
		switch runtime.GOOS {
		case "windows":
			_ = procutil.Command("rundll32", "url.dll,FileProtocolHandler", body.URL).Start()
		case "darwin":
			_ = procutil.Command("open", body.URL).Start()
		default:
			_ = procutil.Command("xdg-open", body.URL).Start()
		}
		c.JSON(200, gin.H{"code": 0})
	})

	// ---- Home / monitor / stats / warp ----
	admin.GET("/system/monitor", func(c *gin.Context) {
		snap, err := engine.CollectMonitor(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": snap})
	})
	admin.GET("/system/capabilities", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": engine.HostCapabilities()})
	})
	admin.GET("/register-stats", func(c *gin.Context) {
		stt, err := a.Accounts.RegisterStats(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		// enrich with live status
		live := a.Engine.Status(c.Request.Context())
		c.JSON(200, gin.H{"code": 0, "data": gin.H{
			"accounts_total":    stt.AccountsTotal,
			"accounts_success":  stt.AccountsSuccess,
			"accounts_imported": stt.AccountsImported,
			"avg_per_min":       stt.AvgPerMin,
			"fastest_sec":       stt.FastestSec,
			"slowest_sec":       stt.SlowestSec,
			"median_sec":        stt.MedianSec,
			"last_1h_count":     stt.Last1hCount,
			"last_24h_count":    stt.Last24hCount,
			"window_minutes":    stt.WindowMinutes,
			"live_running":      live.Running,
			"live_success":      live.Success,
			"live_fail":         live.Fail,
			"live_completed":    live.Completed,
			"live_total":        live.Total,
			"live_elapsed_sec":  live.ElapsedSec,
		}})
	})
	admin.GET("/warp/instances", func(c *gin.Context) {
		list, err := a.Warp.List(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		// 顺带把代理池里的 WARP 条目对齐一次。
		//
		// 需要在这里做而不只在创建时做，是为了覆盖两种情况：
		//   1. 升级迁移——本版本之前创建的 WARP 实例只写进了 register_urls，
		//      不进池；不在这里补一次，老用户要删掉重建才能看到。
		//   2. 容器被外部删除（docker rm / Docker 管理页）——实例列表已经没了，
		//      但池里还留着僵尸出口。
		// SyncWarpProxyEntries 幂等，无变化时不会产生多余写入。
		if a.Accounts != nil {
			if _, sErr := a.Accounts.SyncWarpProxyEntries(c.Request.Context(), list); sErr != nil {
				log.Printf("[warp] 对齐代理池失败（不影响实例列表）: %v", sErr)
			}
		}
		c.JSON(200, gin.H{"code": 0, "data": list})
	})
	admin.GET("/warp/license", func(c *gin.Context) {
		key, err := a.Warp.GetLicense(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{
			"license_key": key,
			"has_license": strings.TrimSpace(key) != "",
		}})
	})
	admin.PUT("/warp/license", func(c *gin.Context) {
		var body struct {
			License string `json:"license_key"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Warp.SaveLicense(c.Request.Context(), body.License); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	admin.GET("/warp/keys", func(c *gin.Context) {
		probe := c.Query("probe") == "1" || c.Query("probe") == "true"
		cat, err := a.Warp.GetKeyCatalog(c.Request.Context(), probe)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": cat})
	})
	admin.PUT("/warp/keys/selection", func(c *gin.Context) {
		var body struct {
			Mode           string `json:"mode"`
			Key            string `json:"key"`
			LicenseKey     string `json:"license_key"`
			RememberCustom bool   `json:"remember_custom"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		key := strings.TrimSpace(body.Key)
		if key == "" {
			key = strings.TrimSpace(body.LicenseKey)
		}
		remember := body.RememberCustom
		if body.Mode == "custom" && key != "" {
			remember = true
		}
		cat, err := a.Warp.AutoSaveKeySelection(c.Request.Context(), body.Mode, key, remember)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": cat})
	})
	admin.POST("/warp/keys/probe", func(c *gin.Context) {
		var body struct {
			Key string `json:"key"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		info, err := a.Warp.ProbeKey(c.Request.Context(), body.Key)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": info})
	})
	admin.POST("/warp/deploy", func(c *gin.Context) {
		var body struct {
			Count   int    `json:"count"`
			License string `json:"license_key"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Count <= 0 {
			body.Count = 1
		}
		// if no license in body, Deploy loads saved license automatically
		created, err := a.Warp.Deploy(c.Request.Context(), body.Count, body.License)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "data": created})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": created})
	})
	admin.POST("/warp/remove", func(c *gin.Context) {
		var body struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Warp.Remove(c.Request.Context(), body.Name); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	admin.POST("/warp/refresh", func(c *gin.Context) {
		var body struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if body.Name == "" || body.Name == "all" {
			list, err := a.Warp.RefreshAll(c.Request.Context())
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, gin.H{"code": 0, "data": list})
			return
		}
		it, err := a.Warp.RefreshIP(c.Request.Context(), body.Name)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": it})
	})
	admin.POST("/warp/restart-all", func(c *gin.Context) {
		list, err := a.Warp.RestartAll(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": list})
	})
	admin.POST("/warp/sync-proxy-pool", func(c *gin.Context) {
		data, err := a.Warp.SyncProxyPool(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": data})
	})

	admin.GET("/warp/global-proxy", func(c *gin.Context) {
		cfg, err := a.Warp.GetGlobalProxy(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": cfg})
	})
	admin.PUT("/warp/global-proxy", func(c *gin.Context) {
		var cfg engine.GlobalProxyConfig
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		out, err := a.Warp.SaveGlobalProxy(c.Request.Context(), cfg)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})

	admin.PUT("/warp/instances/:name/upstream", func(c *gin.Context) {
		var body struct {
			Mode     string `json:"upstream_proxy_mode"`
			Upstream string `json:"upstream_proxy"`
			Recreate *bool  `json:"recreate"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		rec := true
		if body.Recreate != nil {
			rec = *body.Recreate
		}
		inst, err := a.Warp.UpdateInstanceUpstream(c.Request.Context(), c.Param("name"), body.Mode, body.Upstream, rec)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": inst})
	})
	admin.POST("/warp/instances/:name/recreate", func(c *gin.Context) {
		inst, err := a.Warp.RecreateOne(c.Request.Context(), c.Param("name"))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": inst})
	})

	// ---- Grok register ----
	gr := admin.Group("/grok-register")
	gr.GET("/config", func(c *gin.Context) {
		cfg, err := a.Engine.GetConfig(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": cfg})
	})
	// 同步打码并发到运行中的引擎（web 版「同步打码并发」的桌面实现）：
	// 直接调各引擎 /admin/workers 热调整，免重启 docker/重建容器。
	// provider 缺省 = 全部引擎（ezsolver + auralith 两个地址；veloraturn
	// 与 ezsolver 同 URL 兼容协议）。返回各引擎结果。
	gr.POST("/sync-workers", func(c *gin.Context) {
		var body struct {
			Concurrency int    `json:"concurrency"`
			Provider    string `json:"provider"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Concurrency < 1 {
			body.Concurrency = 1
		}
		cfg, err := a.Engine.GetConfig(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		prov := strings.ToLower(strings.TrimSpace(body.Provider))
		var targets []engine.SyncTarget
		switch prov {
		case "auralith", "au":
			targets = append(targets, engine.SyncTarget{ID: "auralith", URL: cfg.AuralithURL, APIKey: cfg.AuralithAPIKey, TimeoutSec: cfg.AuralithTimeoutSec})
		case "ezsolver", "veloraturn":
			targets = append(targets, engine.SyncTarget{ID: prov, URL: cfg.EzSolverURL, APIKey: cfg.EzSolverAPIKey, TimeoutSec: cfg.EzSolverTimeoutSec})
		default:
			targets = append(targets,
				engine.SyncTarget{ID: "ezsolver", URL: cfg.EzSolverURL, APIKey: cfg.EzSolverAPIKey, TimeoutSec: cfg.EzSolverTimeoutSec},
				engine.SyncTarget{ID: "veloraturn", URL: replacePort(cfg.EzSolverURL, 8193), APIKey: cfg.EzSolverAPIKey, TimeoutSec: cfg.EzSolverTimeoutSec},
				engine.SyncTarget{ID: "auralith", URL: cfg.AuralithURL, APIKey: cfg.AuralithAPIKey, TimeoutSec: cfg.AuralithTimeoutSec},
			)
		}
		// 打码并发 = 注册并发 + 2 余量（无上限）：本地引擎 MAX_WORKERS
		// 决定浏览器数，并发打码占满容量会排队（实测 6 并发时求解被拖到
		// ~19s/个）——留 2 个空位给重试/波动，吞吐显著提升；资源充足
		// （本机 32G/容器 15.6G 用 2G），引擎 auto budget（CPU 高水位）
		// 自动兜底，不会 OOM。
		syncN := body.Concurrency + 2
		if syncN < 1 {
			syncN = 1
		}
		results := a.Engine.SyncCaptchaWorkers(targets, syncN)
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"results": results, "concurrency": syncN, "requested": body.Concurrency}})
	})

	// ── iCloud Privacy Mail 取码平台网关 ──
	// 平台账号登录（持 cookie 会话）+ 账号/邮箱代理 + 本地邮箱池同步。
	// 注册页 email_mode=icloud_api 消费本地池（按账号过滤）。
	gr.POST("/icloud/panel/start", func(c *gin.Context) {
		var body struct {
			Bin string `json:"bin"`
		}
		_ = c.ShouldBindJSON(&body)
		url, err := a.Engine.Icloud().StartPanel(c.Request.Context(), body.Bin)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"url": url, "status": a.Engine.Icloud().PanelStatus()}})
	})
	gr.POST("/icloud/panel/stop", func(c *gin.Context) {
		_ = a.Engine.Icloud().StopPanel()
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"status": a.Engine.Icloud().PanelStatus()}})
	})
	gr.GET("/icloud/panel/status", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": a.Engine.Icloud().PanelStatus()})
	})
	gr.POST("/icloud/apple-login/start", func(c *gin.Context) {
		var body struct {
			BaseURL  string `json:"base_url"`
			AppleID  string `json:"apple_id"`
			Password string `json:"password"`
			Method   string `json:"two_factor_method"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		out, err := a.Engine.Icloud().AppleLoginStart(c.Request.Context(), body.BaseURL, body.AppleID, body.Password, body.Method)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})
	gr.POST("/icloud/apple-login/2fa", func(c *gin.Context) {
		var body struct {
			BaseURL   string `json:"base_url"`
			PendingID string `json:"pending_id"`
			Code      string `json:"code"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		out, err := a.Engine.Icloud().AppleLogin2FA(c.Request.Context(), body.BaseURL, body.PendingID, body.Code)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})
	gr.POST("/icloud/login", func(c *gin.Context) {
		var body struct {
			BaseURL  string `json:"base_url"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Engine.Icloud().Login(c.Request.Context(), body.BaseURL, body.Username, body.Password); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"logged_in": true}})
	})
	gr.POST("/icloud/health", func(c *gin.Context) {
		var body struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// webview 跨域 fetch 会被 CORS 拦截，健康检查走后端代理
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet,
			strings.TrimRight(body.BaseURL, "/")+"/api/v1/health", nil)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(body.APIKey) != "" {
			req.Header.Set("Authorization", "Bearer "+body.APIKey)
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			c.JSON(400, gin.H{"error": "无法连接平台: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var d map[string]any
		_ = json.Unmarshal(raw, &d)
		if resp.StatusCode >= 400 {
			msg, _ := d["error"].(map[string]any)
			m := ""
			if msg != nil {
				m, _ = msg["message"].(string)
			}
			if m == "" {
				m = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			c.JSON(400, gin.H{"error": m})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": d})
	})
	gr.POST("/icloud/logout", func(c *gin.Context) {
		var body struct {
			BaseURL string `json:"base_url"`
		}
		_ = c.ShouldBindJSON(&body)
		a.Engine.Icloud().Logout(body.BaseURL)
		c.JSON(200, gin.H{"code": 0})
	})
	gr.GET("/icloud/accounts", func(c *gin.Context) {
		base := c.Query("base_url")
		accs, err := a.Engine.Icloud().Accounts(c.Request.Context(), base)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"accounts": accs}})
	})
	gr.GET("/icloud/mailboxes", func(c *gin.Context) {
		base := c.Query("base_url")
		mbs, err := a.Engine.Icloud().Mailboxes(c.Request.Context(), base)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"mailboxes": mbs}})
	})
	gr.POST("/icloud/mailboxes/create", func(c *gin.Context) {
		var body struct {
			BaseURL   string `json:"base_url"`
			AccountID string `json:"account_id"`
			Count     int    `json:"count"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if body.Count < 1 {
			body.Count = 1
		}
		n, err := a.Engine.Icloud().CreateMailboxes(c.Request.Context(), body.BaseURL, body.AccountID, body.Count)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"created": n}})
	})
	gr.POST("/icloud/open-apple", func(c *gin.Context) {
		// webview 不处理 target=_blank，由后端调系统浏览器打开
		// （白名单：仅 Apple 账号管理页）
		url := "https://account.apple.com/sign-in"
		if runtime.GOOS == "windows" {
			_ = procutil.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		} else {
			_ = procutil.Command("open", url).Start()
		}
		c.JSON(200, gin.H{"code": 0})
	})
	gr.GET("/icloud/forward-target", func(c *gin.Context) {
		fwd, err := a.Engine.Icloud().FetchForwardTarget(c.Request.Context())
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"email": fwd}})
	})
	gr.POST("/icloud/messages/delete", func(c *gin.Context) {
		var body struct {
			BaseURL   string `json:"base_url"`
			AccountID string `json:"account_id"`
			Keyword   string `json:"keyword"`
		}
		_ = c.ShouldBindJSON(&body)
		n, err := a.Engine.Icloud().DeleteMessagesByKeyword(c.Request.Context(), body.BaseURL, body.AccountID, body.Keyword)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"deleted": n}})
	})
	gr.POST("/icloud/imap-login/check", func(c *gin.Context) {
		var body struct {
			BaseURL   string `json:"base_url"`
			AccountID string `json:"account_id"`
		}
		_ = c.ShouldBindJSON(&body)
		out, err := a.Engine.Icloud().CheckIMAPLogin(c.Request.Context(), body.BaseURL, body.AccountID)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})
	gr.POST("/icloud/imap-login/save", func(c *gin.Context) {
		var body struct {
			BaseURL     string `json:"base_url"`
			AccountID   string `json:"account_id"`
			Email       string `json:"email"`
			AppPassword string `json:"app_password"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Engine.Icloud().SaveIMAPLogin(c.Request.Context(), body.BaseURL, body.AccountID, body.Email, body.AppPassword); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	gr.POST("/icloud/mailboxes/delete", func(c *gin.Context) {
		var body struct {
			BaseURL string `json:"base_url"`
			ID      string `json:"id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Engine.Icloud().DeleteMailbox(c.Request.Context(), body.BaseURL, body.ID); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	gr.POST("/icloud/sync-pool", func(c *gin.Context) {
		var body struct {
			BaseURL string `json:"base_url"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		accs, _ := a.Engine.Icloud().Accounts(c.Request.Context(), body.BaseURL)
		added, err := a.Engine.Icloud().SyncPool(c.Request.Context(), body.BaseURL, accs)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		pool := a.Engine.Icloud().Pool(nil)
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"added": added, "total": len(pool.Entries)}})
	})
	gr.GET("/icloud/pool", func(c *gin.Context) {
		ids := []string{}
		if q := c.Query("accounts"); q != "" {
			ids = append(ids, strings.Split(q, ",")...)
		}
		pool := a.Engine.Icloud().Pool(ids)
		c.JSON(200, gin.H{"code": 0, "data": pool})
	})
	gr.POST("/icloud/pool/release", func(c *gin.Context) {
		var body struct {
			Email string `json:"email"`
		}
		_ = c.ShouldBindJSON(&body)
		a.Engine.Icloud().ReleasePoolEmail(body.Email)
		c.JSON(200, gin.H{"code": 0})
	})
	gr.POST("/icloud/pool/clear", func(c *gin.Context) {
		a.Engine.Icloud().ResetPool()
		c.JSON(200, gin.H{"code": 0})
	})

	gr.PUT("/config", func(c *gin.Context) {
		raw, rerr := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if rerr != nil {
			c.JSON(400, gin.H{"error": rerr.Error()})
			return
		}
		var cfg engine.Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// 局部更新保护：JSON 里「字段缺失」和「显式传零值」在 Go 里都是零值，
		// 无法区分。所以对有意义的零值字段必须按「键是否出现」来判断。
		var present map[string]json.RawMessage
		hasPresent := json.Unmarshal(raw, &present) == nil
		if hasPresent {
			// 打码并发上限：0 是合法值（=跟随注册并发）。缺失时标成 -1，
			// SaveConfig 的 <0 分支会保留旧值。
			if _, ok := present["ezsolver_max_concurrency"]; !ok {
				cfg.EzSolverMaxConcur = -1
			}
			if _, ok := present["auralith_max_concurrency"]; !ok {
				cfg.AuralithMaxConcur = -1
			}
			// bool 字段没有 -1 这种哨兵，缺失时直接从当前配置回填。
			// 尤其是 skip_verify：被误写成 false 会重新打开注册尾部的 device
			// OAuth 验活，每个账号多花约 13.5 秒（实测 12.6s → 30s），
			// 而入库转换本来就会做一次 OAuth，那次验活是重复的。
			if cur, err := a.Engine.GetConfig(c.Request.Context()); err == nil {
				if _, ok := present["skip_verify"]; !ok {
					cfg.SkipVerify = cur.SkipVerify
				}
				if _, ok := present["auto_import"]; !ok {
					cfg.AutoImport = cur.AutoImport
				}
			}
		}
		out, err := a.Engine.SaveConfig(c.Request.Context(), cfg)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})
	gr.POST("/start", func(c *gin.Context) {
		var in engine.StartInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// merge proxy pool if not provided（显式关池时不回填，否则「直连」是假的）
		if in.PoolOptOut() {
			in.ProxyURLs = nil
		} else if len(in.ProxyURLs) == 0 && a.Accounts != nil {
			if pool, err := a.Accounts.GetProxyPool(c.Request.Context()); err == nil && len(pool.URLs) > 0 {
				in.ProxyURLs = pool.URLs
			}
		}
		if err := a.Engine.Start(c.Request.Context(), in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"status": "started"}})
	})
	gr.POST("/stop", func(c *gin.Context) {
		_ = a.Engine.Stop(c.Request.Context())
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"status": "stopped"}})
	})
	gr.GET("/status", func(c *gin.Context) {
		st := a.Engine.Status(c.Request.Context())
		if a.Accounts != nil {
			if list, err := a.Accounts.List(c.Request.Context()); err == nil {
				// attach counts without full secrets
				stMap := map[string]any{
					"running": st.Running, "stopping": st.Stopping, "total": st.Total,
					"completed": st.Completed, "success": st.Success, "fail": st.Fail,
					"message": st.Message, "last_email": st.LastEmail, "last_error": st.LastError,
					"elapsed_sec": st.ElapsedSec, "engine": st.Engine, "captcha_provider": st.Captcha,
					"results": st.Results, "accounts_count": len(list),
					// 自动暂停状态必须一并透出，否则前端倒计时永远拿不到值
					"auto_paused": st.AutoPaused, "auto_pause_remaining_sec": st.AutoPauseRemainingSec,
					// 自动入库进度/数量/耗时：注册页此前完全看不到入库情况，
					// 开着「自动入库」跑一批只能从日志里一行行找 [自动入库]。
					"auto_import_enabled": st.AutoImportEnabled,
					"import_done":         st.ImportDone,
					"import_success":      st.ImportSuccess,
					"import_failed":       st.ImportFailed,
					"import_elapsed_ms":   st.ImportElapsedMS,
					"import_last_email":   st.ImportLastEmail,
					"import_last_error":   st.ImportLastError,
					// 待入库积压（有 SSO 无 OAuth）——自动入库失败的账号会留在这里。
					// 复用上面已加载的 list，不再触发第二次 store 读取 + 排序。
					"pending_import_count": engine.CountPendingImports(list),
					// 每分钟成功/失败数（最近 30 分钟，失败不算成功）。
					"per_min": st.PerMin,
					// 打码闸/实际 workers（启动时采样，解释「并发 10 为什么活跃打码少」）
					"captcha_gate": st.CaptchaGate,
					"captcha_live": st.CaptchaLive,
				}
				c.JSON(200, gin.H{"code": 0, "data": stMap})
				return
			}
		}
		c.JSON(200, gin.H{"code": 0, "data": st})
	})
	gr.GET("/logs", func(c *gin.Context) { c.JSON(200, gin.H{"code": 0, "data": a.Engine.GetLogs()}) })
	gr.POST("/logs/clear", func(c *gin.Context) { a.Engine.ClearLogs(); c.JSON(200, gin.H{"code": 0}) })

	// proxy pool
	gr.GET("/proxy-pool", func(c *gin.Context) {
		p, err := a.Accounts.GetProxyPool(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": p})
	})
	gr.PUT("/proxy-pool", func(c *gin.Context) {
		var body struct {
			URLs    []string            `json:"urls"`
			Text    string              `json:"text"`
			Entries []engine.ProxyEntry `json:"entries"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		var p engine.ProxyPool
		if len(body.Entries) > 0 {
			p = engine.ProxyPool{Entries: body.Entries}
		} else if body.Text != "" {
			// 支持 "url #备注" / "url|备注" 解析
			p = engine.ProxyPool{Entries: engine.ParseProxyTextEntries(body.Text)}
		} else {
			p = engine.ProxyPool{URLs: body.URLs}
		}
		saved, err := a.Accounts.SaveProxyPool(c.Request.Context(), p)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": saved})
	})
	gr.POST("/proxy-pool/test", func(c *gin.Context) {
		var body struct {
			URLs []string `json:"urls"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		results := engine.TestProxyPool(c.Request.Context(), body.URLs)
		c.JSON(200, gin.H{"code": 0, "data": results})
	})
	gr.GET("/proxy-settings", func(c *gin.Context) {
		ps, err := a.Accounts.GetProxySettings(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": ps})
	})
	gr.PUT("/proxy-settings", func(c *gin.Context) {
		var body engine.ProxySettings
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// also accept text fields
		var raw map[string]any
		// body already bound; ok
		ps, err := a.Accounts.SaveProxySettings(c.Request.Context(), body)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": ps})
		_ = raw
	})

	// accounts vault
	gr.GET("/accounts", func(c *gin.Context) {
		list, err := a.Accounts.List(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		// full secrets intentionally returned for desktop local use
		c.JSON(200, gin.H{"code": 0, "data": list})
	})
	gr.POST("/accounts/delete", func(c *gin.Context) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		n, err := a.Accounts.Delete(c.Request.Context(), body.IDs)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"deleted": n}})
	})
	gr.POST("/accounts/clear", func(c *gin.Context) {
		if err := a.Accounts.Clear(c.Request.Context()); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	// 单账号详情（含完整凭据，桌面本地用）
	gr.GET("/accounts/:id", func(c *gin.Context) {
		acc, err := a.Accounts.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(404, gin.H{"code": 404, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": acc})
	})
	// 单账号编辑（仅传入字段生效；用 POST 避免 CORS 预检差异）
	gr.POST("/accounts/:id/update", func(c *gin.Context) {
		var patch engine.AccountPatch
		if err := c.ShouldBindJSON(&patch); err != nil {
			c.JSON(400, gin.H{"code": 400, "error": err.Error()})
			return
		}
		acc, err := a.Accounts.Update(c.Request.Context(), c.Param("id"), patch)
		if err != nil {
			c.JSON(400, gin.H{"code": 400, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": acc})
	})
	// 单账号测试对话（非流式，一次性返回）
	gr.POST("/accounts/:id/test", func(c *gin.Context) {
		var body engine.TestChatOptions
		_ = c.ShouldBindJSON(&body)
		body.Stream = false
		res, err := a.Accounts.TestChat(c.Request.Context(), c.Param("id"), body, nil)
		if err != nil {
			c.JSON(400, gin.H{"code": 400, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": res})
	})
	// 单账号测试对话（SSE 流式，前端终端实时打字）
	gr.POST("/accounts/:id/test-stream", func(c *gin.Context) {
		var body engine.TestChatOptions
		_ = c.ShouldBindJSON(&body)
		body.Stream = true

		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(200)

		flusher, _ := c.Writer.(http.Flusher)
		send := func(payload gin.H) {
			b, err := json.Marshal(payload)
			if err != nil {
				return
			}
			_, _ = c.Writer.Write([]byte("data: "))
			_, _ = c.Writer.Write(b)
			_, _ = c.Writer.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}

		send(gin.H{"type": "test_start"})
		res, err := a.Accounts.TestChat(c.Request.Context(), c.Param("id"), body, func(delta string) {
			send(gin.H{"type": "content", "text": delta})
		})
		if err != nil {
			send(gin.H{"type": "test_complete", "success": false, "error": err.Error(), "latency_ms": res.LatencyMs})
			return
		}
		send(gin.H{"type": "test_complete", "success": true, "model": res.Model, "latency_ms": res.LatencyMs,
			"prompt_tokens": res.PromptTokens, "completion_tokens": res.CompletionTokens, "total_tokens": res.TotalTokens,
			"proxy_used": res.ProxyUsed})
	})
	// 单账号可用模型（上游 /models；失败时回落内置列表，前端仍可选模型）
	gr.GET("/accounts/:id/models", func(c *gin.Context) {
		useProxy := c.Query("use_proxy") == "true"
		models, err := a.Accounts.ListModels(c.Request.Context(), c.Param("id"), useProxy)
		if err != nil {
			fallback := make([]engine.ModelInfo, 0, len(xai.DefaultModels()))
			for _, m := range xai.DefaultModels() {
				fallback = append(fallback, engine.ModelInfo{ID: m.ID, DisplayName: m.DisplayName})
			}
			c.JSON(200, gin.H{"code": 0, "data": gin.H{"models": fallback, "fallback": true, "error": err.Error()}})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"models": models, "fallback": false}})
	})
	// 单账号凭证校验（拉 /models，比对话更快更省）
	gr.POST("/accounts/:id/verify", func(c *gin.Context) {
		var body struct {
			UseProxy bool `json:"use_proxy"`
		}
		_ = c.ShouldBindJSON(&body)
		n, err := a.Accounts.VerifyCredential(c.Request.Context(), c.Param("id"), body.UseProxy)
		if err != nil {
			c.JSON(400, gin.H{"code": 400, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"models": n}})
	})
	// 单账号重新换取 OAuth 凭证（已有凭证也强制覆盖）
	gr.POST("/accounts/:id/refresh-oauth", func(c *gin.Context) {
		var body struct {
			UseProxy bool `json:"use_proxy"`
		}
		_ = c.ShouldBindJSON(&body)
		acc, err := a.Accounts.RefreshOAuth(c.Request.Context(), c.Param("id"), body.UseProxy)
		if err != nil {
			c.JSON(400, gin.H{"code": 400, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": acc})
	})
	gr.POST("/accounts/convert", func(c *gin.Context) {
		var body struct {
			IDs       []string `json:"ids"`
			ProxyMode string   `json:"import_proxy_mode"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// 留空交给 ConvertToOAuth 读「代理设置 → 导入出口」，不再默认强走注册代理
		res, err := a.Accounts.ConvertToOAuth(c.Request.Context(), body.IDs, body.ProxyMode)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": res})
	})
	// 异步入库 + 进度轮询。
	// 同步那个接口 23 个账号最坏要几分钟才返回（并发 4，每个最多 3 次退避重试），
	// 期间前端只能把按钮切成「入库转换中…」干等，看不到跑到第几个。
	gr.POST("/accounts/convert-async", func(c *gin.Context) {
		var body struct {
			IDs       []string `json:"ids"`
			ProxyMode string   `json:"import_proxy_mode"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		prog, err := a.Accounts.ConvertToOAuthAsync(body.IDs, body.ProxyMode)
		if err != nil {
			c.JSON(409, gin.H{"code": 1, "error": err.Error(), "data": prog})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": prog})
	})
	// 注意路径：不能放在 /accounts/ 下面。gr.GET("/accounts/:id") 已经占了那一层，
	// gin 会把 "import-progress" 当成账号 ID 交给 :id 处理器，返回
	// 404「账号不存在」。实测踩过，改成同级独立路径。
	gr.GET("/accounts-import-progress", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": a.Accounts.ImportProgress()})
	})
	gr.POST("/accounts/mark-imported", func(c *gin.Context) {
		var body struct {
			IDs      []string `json:"ids"`
			Imported bool     `json:"imported"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		n, err := a.Accounts.MarkImported(c.Request.Context(), body.IDs, body.Imported)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"updated": n}})
	})
	gr.GET("/accounts/export", func(c *gin.Context) {
		format := c.DefaultQuery("format", "json")
		successOnly := c.DefaultQuery("success_only", "true") != "false"
		body, ctype, err := a.Accounts.Export(c.Request.Context(), format, successOnly)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Type", ctype+"; charset=utf-8")
		c.Header("Content-Disposition", "attachment; filename=umbraforge-accounts."+extFor(format))
		c.String(200, body)
	})
	// Native Save-As dialog (desktop): user picks path, server writes file.
	gr.POST("/accounts/export-save", func(c *gin.Context) {
		var body struct {
			Format      string `json:"format"`
			SuccessOnly *bool  `json:"success_only"`
			DefaultName string `json:"default_name"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if body.Format == "" {
			body.Format = "json"
		}
		successOnly := true
		if body.SuccessOnly != nil {
			successOnly = *body.SuccessOnly
		}
		def := body.DefaultName
		if def == "" {
			def = "umbraforge-accounts." + extFor(body.Format)
		}
		content, _, err := a.Accounts.Export(c.Request.Context(), body.Format, successOnly)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		path, err := engine.ChooseSavePath("导出账号 — 选择保存位置", def)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		if path == "" {
			c.JSON(200, gin.H{"code": 0, "data": gin.H{"cancelled": true}})
			return
		}
		// ensure extension
		if filepath.Ext(path) == "" {
			path = path + "." + extFor(body.Format)
		}
		if err := engine.WriteExportFile(path, content); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"cancelled": false, "path": path, "bytes": len(content)}})
	})
	// import-once = mark all success as imported (local vault semantics)
	gr.POST("/import", func(c *gin.Context) {
		n, err := a.Accounts.MarkImported(c.Request.Context(), nil, true)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"imported": n, "failed": 0}})
	})

	// ---- EDU ----
	edu := admin.Group("/edu-email")
	edu.GET("", func(c *gin.Context) {
		st, err := a.Edu.List(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": st})
	})
	edu.POST("/workers", func(c *gin.Context) {
		var in engine.EduWorker
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		out, err := a.Edu.Upsert(c.Request.Context(), in)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})
	edu.DELETE("/workers/:id", func(c *gin.Context) {
		if err := a.Edu.Delete(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	edu.POST("/zones", func(c *gin.Context) {
		var body struct {
			AccountID         string `json:"cf_account_id"`
			APIToken          string `json:"cf_api_token"`
			Probe             *bool  `json:"probe"`
			ProbeEmailRouting *bool  `json:"probe_email_routing"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		probe := true
		if body.ProbeEmailRouting != nil {
			probe = *body.ProbeEmailRouting
		} else if body.Probe != nil {
			probe = *body.Probe
		}
		zones, err := a.Edu.ListCFZones(c.Request.Context(), body.AccountID, body.APIToken, probe)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": zones})
	})
	edu.POST("/generate-addresses", func(c *gin.Context) {
		var body struct {
			Domain          string `json:"domain"`
			Count           int    `json:"count"`
			UseEduSubdomain *bool  `json:"use_edu_subdomain"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		useEdu := true
		if body.UseEduSubdomain != nil {
			useEdu = *body.UseEduSubdomain
		}
		c.JSON(200, gin.H{"code": 0, "data": a.Edu.GenerateAddresses(body.Domain, body.Count, useEdu)})
	})
	edu.POST("/provision", func(c *gin.Context) {
		var req cfemail.ProvisionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		res, err := a.Edu.Provision(c.Request.Context(), req)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": res})
	})
	edu.POST("/provision-batch", func(c *gin.Context) {
		var body struct {
			Domains     []string `json:"domains"`
			AccountID   string   `json:"cf_account_id"`
			APIToken    string   `json:"cf_api_token"`
			CallbackURL string   `json:"callback_url"`
			NamePrefix  string   `json:"name_prefix"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		res := a.Edu.ProvisionBatch(c.Request.Context(), body.AccountID, body.APIToken, body.CallbackURL, body.NamePrefix, body.Domains)
		c.JSON(200, gin.H{"code": 0, "data": res})
	})

	// Import already CF-open domains into EDU pool without full re-provision
	edu.POST("/import-open", func(c *gin.Context) {
		var req cfemail.ImportOpenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		res, err := a.Edu.ImportOpen(c.Request.Context(), req)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": res})
	})
	edu.POST("/import-open-batch", func(c *gin.Context) {
		var body struct {
			Domains     []string `json:"domains"`
			AccountID   string   `json:"cf_account_id"`
			APIToken    string   `json:"cf_api_token"`
			CallbackURL string   `json:"callback_url"`
			NamePrefix  string   `json:"name_prefix"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		res := a.Edu.ImportOpenBatch(c.Request.Context(), body.AccountID, body.APIToken, body.CallbackURL, body.NamePrefix, body.Domains)
		c.JSON(200, gin.H{"code": 0, "data": res})
	})
	edu.POST("/callback", func(c *gin.Context) {
		var in struct {
			Email string `json:"email"`
			Code  string `json:"code"`
			Body  string `json:"body"`
			Key   string `json:"key"`
		}
		_ = c.ShouldBindJSON(&in)
		ok := a.Edu.ReceiveCallback(c.Request.Context(), in.Email, in.Code, in.Body, in.Key)
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"ok": ok}})
	})
	edu.GET("/docs", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": eduDocs()})
	})
	// CF multi-account credentials (permanent Application Support store)
	edu.GET("/cf-accounts", func(c *gin.Context) {
		st, err := a.Edu.ListCFAccounts(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": st})
	})
	edu.POST("/cf-accounts", func(c *gin.Context) {
		var in engine.CFAccount
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		out, err := a.Edu.UpsertCFAccount(c.Request.Context(), in)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": out})
	})
	edu.DELETE("/cf-accounts/:id", func(c *gin.Context) {
		if err := a.Edu.DeleteCFAccount(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	edu.POST("/cf-accounts/:id/activate", func(c *gin.Context) {
		st, err := a.Edu.SetActiveCFAccount(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": st})
	})
	// Resolve raw secrets for active/selected account (desktop local only)
	edu.GET("/cf-accounts/:id/raw", func(c *gin.Context) {
		acc, ok, err := a.Edu.GetCFAccountRaw(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(404, gin.H{"error": "账号不存在"})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": acc})
	})

	// plugins
	pl := admin.Group("/plugins")
	pl.GET("", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"catalog": a.Plugins.Catalog(), "status": a.Plugins.ListStatus()}})
	})
	pl.GET("/status", func(c *gin.Context) { c.JSON(200, gin.H{"code": 0, "data": a.Plugins.ListStatus()}) })
	pl.POST("/install", func(c *gin.Context) {
		var req plugins.InstallRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// mode=docker 是「切换/判断」语义（同步）：已部署 → 秒级切换；
		// 未部署 → 明确报错并指路「Docker 管理 → 打码引擎」一键部署。
		// 真正的部署入口是 /docker/captcha/deploy-all（一键部署全部，异步带进度）。
		if req.Mode == plugins.ModeDocker {
			st, err := a.Plugins.Install(req)
			if err != nil {
				c.JSON(500, gin.H{"code": 1, "error": err.Error(), "data": st})
				return
			}
			c.JSON(200, gin.H{"code": 0, "data": st})
			return
		}
		st, err := a.Plugins.Install(req)
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error(), "data": st})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": st})
	})
	pl.POST("/stop", func(c *gin.Context) {
		var req struct {
			ID plugins.PluginID `json:"id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		st, err := a.Plugins.Stop(req.ID)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "data": st})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": st})
	})
	pl.POST("/ensure-docker", func(c *gin.Context) {
		msg, err := a.Plugins.EnsureDocker()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "message": msg})
			return
		}
		c.JSON(200, gin.H{"code": 0, "message": msg})
	})
	// docker-install：异步安装 Docker（带进度），轮询 docker-task 看进度
	pl.POST("/docker-install", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": a.Plugins.EnsureDockerAsync()})
	})
	// docker-task：Docker 安装 / 打码容器部署的异步进度快照
	pl.GET("/docker-task", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": a.Plugins.Tasks()})
	})
	pl.POST("/uninstall-docker", func(c *gin.Context) {
		var body struct {
			RemoveAllContainers bool `json:"remove_all_containers"`
		}
		_ = c.ShouldBindJSON(&body)
		// also clear warp instance records so UI stays consistent
		if a.Warp != nil {
			list, _ := a.Warp.List(c.Request.Context())
			for _, it := range list {
				_ = a.Warp.Remove(c.Request.Context(), it.Name)
			}
		}
		data, err := a.Plugins.UninstallDocker(body.RemoveAllContainers)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "data": data})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": data})
	})

	// 删除 Docker 安装器下载缓存（卸载后弹窗询问的清理动作）。
	// keep_one=true 保留最新一份（安装前整理用），false 全部删除。
	pl.POST("/docker-clean-installers", func(c *gin.Context) {
		var body struct {
			KeepOne bool `json:"keep_one"`
		}
		_ = c.ShouldBindJSON(&body)
		removed, freedMB := a.Plugins.CleanDockerInstallerCaches(body.KeepOne)
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"removed": removed, "freed_mb": freedMB}})
	})

	// ── Docker 管理：运行时规格（自动跟随真机）+ 容器/镜像管理 ──
	//
	// 规格这一层存在的理由：macOS/Windows 上容器跑在 Docker VM 里，拿到的是
	// VM 的配额而不是宿主规格。colima 默认 2 核，宿主 12 核也没用 —— 打码是
	// CPU 密集型，直接决定每分钟能注册几个。这里把探测/推荐/一键应用做成接口。
	dk := admin.Group("/docker")
	dk.GET("/runtime", func(c *gin.Context) {
		c.JSON(200, gin.H{"code": 0, "data": a.Plugins.DockerRuntime()})
	})
	// captcha/deploy-all：一键部署全部打码引擎容器（异步任务，轮询 docker-task 看进度）
	dk.POST("/captcha/deploy-all", func(c *gin.Context) {
		task, err := a.Plugins.DeployAllCaptchaDockerAsync()
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error(), "data": task})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": task})
	})
	// vm/start：装了 Docker 但守护进程连不上时启动/修复虚拟机
	// （colima 裸 start / restart，绝不在这个入口覆写用户规格）
	dk.POST("/vm/start", func(c *gin.Context) {
		msg, err := a.Plugins.StartDockerVM()
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "message": msg})
	})
	dk.POST("/runtime/apply", func(c *gin.Context) {
		var body struct {
			Cores int `json:"cores"` // <=0 用推荐值
			MemMB int `json:"mem_mb"`
		}
		_ = c.ShouldBindJSON(&body)
		c.JSON(200, gin.H{"code": 0, "data": a.Plugins.ApplyDockerVMSpecsAsync(body.Cores, body.MemMB)})
	})
	dk.GET("/containers", func(c *gin.Context) {
		list, err := a.Plugins.ListContainers()
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": list})
	})
	dk.POST("/containers/action", func(c *gin.Context) {
		var body struct {
			Name   string `json:"name"`
			Action string `json:"action"` // start|stop|restart|remove
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Plugins.ContainerActionByName(body.Name, body.Action); err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	dk.GET("/images", func(c *gin.Context) {
		list, err := a.Plugins.ListImages()
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": list})
	})
	dk.POST("/images/remove", func(c *gin.Context) {
		var body struct {
			Ref         string `json:"ref"`
			AllowPublic bool   `json:"allow_public"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := a.Plugins.RemoveImage(body.Ref, body.AllowPublic); err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0})
	})
	dk.GET("/disk", func(c *gin.Context) {
		data, err := a.Plugins.DockerDiskUsage()
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "data": data})
	})
	// 异步卸载 Docker（Windows 走 winget 提权静默，其余复用同步实现）。
	// 界面此前没有卸载入口 —— 而「装了但起不来」在 Windows 上很常见，
	// 用户只能去控制面板，且不知道要顺带清理打码容器。
	dk.POST("/uninstall", func(c *gin.Context) {
		var body struct {
			RemoveAllContainers bool `json:"remove_all_containers"`
		}
		_ = c.ShouldBindJSON(&body)
		c.JSON(200, gin.H{"code": 0, "data": a.Plugins.UninstallDockerAsync(body.RemoveAllContainers)})
	})
	dk.POST("/prune", func(c *gin.Context) {
		msg, err := a.Plugins.PruneDocker()
		if err != nil {
			c.JSON(500, gin.H{"code": 1, "error": err.Error(), "message": msg})
			return
		}
		c.JSON(200, gin.H{"code": 0, "message": msg})
	})

	if a.Static != nil {
		fileServer := http.FileServer(http.FS(a.Static))
		r.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/api/") || path == "/health" {
				c.JSON(404, gin.H{"error": "not found"})
				return
			}
			p := strings.TrimPrefix(path, "/")
			if p == "" {
				p = "index.html"
			}
			if f, err := a.Static.Open(p); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
			c.Request.URL.Path = "/"
			fileServer.ServeHTTP(c.Writer, c.Request)
		})
	}
	return r
}

func extFor(format string) string {
	switch strings.ToLower(format) {
	case "csv":
		return "csv"
	case "full", "txt":
		return "txt"
	// sub2api-sso 是纯文本 SSO 列表（一行一个），其余都是 JSON。
	// newapi 现在导出 new-api 渠道数组（JSON），不再是 NDJSON。
	case "sub2api-sso":
		return "txt"
	default:
		return "json"
	}
}

func eduDocs() map[string]any {
	return map[string]any{
		"title": "EDU / CF Email 使用说明",
		"prereqs": []string{
			"Cloudflare 账号 + 已托管域名（Zone Active）",
			"API Token 具备 Zone/Email Routing/Workers/KV 相关权限",
			"域名开启 Email Routing（或由一键开通自动处理）",
			"CALLBACK_URL 需公网可达（本机桌面可用隧道/frp 暴露 /api/v1/admin/edu-email/callback）",
		},
		"token_perms": []map[string]string{
			{"name": "Account.Cloudflare Workers:Edit", "why": "创建/部署 Email Worker"},
			{"name": "Account.Workers KV Storage:Edit", "why": "创建 KV 命名空间"},
			{"name": "Zone.Email Routing Addresses:Edit", "why": "配置收信地址/规则"},
			{"name": "Zone.Email Routing Rules:Edit", "why": "路由到 Worker"},
			{"name": "Zone.Zone:Read", "why": "列出域名 Zone"},
			{"name": "Zone.Workers Routes:Edit", "why": "绑定路由（如需要）"},
		},
		"links": []map[string]string{
			{"title": "Find Account & Zone ID", "url": "https://developers.cloudflare.com/fundamentals/account/find-account-and-zone-ids/", "desc": "如何查找 Account ID / Zone ID"},
			{"title": "Create API Token", "url": "https://developers.cloudflare.com/fundamentals/api/get-started/create-token/", "desc": "创建 API Token 官方指南"},
			{"title": "API Tokens 控制台", "url": "https://dash.cloudflare.com/profile/api-tokens", "desc": "一键打开 Token 管理页"},
			{"title": "Email Routing", "url": "https://developers.cloudflare.com/email-routing/", "desc": "Cloudflare Email Routing 文档"},
			{"title": "Workers", "url": "https://developers.cloudflare.com/workers/", "desc": "Workers 官方文档"},
			{"title": "Workers KV", "url": "https://developers.cloudflare.com/kv/", "desc": "KV 存储文档"},
			{"title": "Cloudflare Dashboard", "url": "https://dash.cloudflare.com/", "desc": "控制台首页"},
		},
		"steps": []string{
			"填写 Account ID + API Token，点击加载域名",
			"单域名开通 或 勾选批量开通（自动同步进 EDU 池）",
			"在 Grok 注册页邮箱模式选 edu，将使用已启用 Worker",
			"若需 OTP 推送，配置 CALLBACK_URL 指向本服务 /api/v1/admin/edu-email/callback",
		},
	}
}

// replacePort 把 URL 的端口换成 targetPort（veloraturn 复用 ezsolver_url
// 配置但端口固定 8193，全部引擎同步时用它构造 veloraturn 目标）。
func replacePort(raw string, targetPort int) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(targetPort))
	return u.String()
}
