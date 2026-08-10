// Package captcha provides free/paid captcha provider clients used by Grok register.
package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultEzSolverURL is the local EzSolver HTTP endpoint (Xvfb + Chromium).
const DefaultEzSolverURL = "http://127.0.0.1:8192"

// GrokTurnstileSiteKey is the xAI signup Turnstile sitekey used by Kiro/Grok register.
const GrokTurnstileSiteKey = "0x4AAAAAAAhr9JGVDZbrZOo0"

// EzSolverClient talks to a free EzSolver instance (POST /solve).
type EzSolverClient struct {
	BaseURL    string
	APIKey     string // for public/external hosts; private/loopback free on solver
	HTTPClient *http.Client
	TimeoutSec int
}

// NewEzSolverClient creates a client. baseURL empty → DefaultEzSolverURL.
func NewEzSolverClient(baseURL string, timeoutSec int) *EzSolverClient {
	return NewEzSolverClientWithKey(baseURL, timeoutSec, "")
}

// NewEzSolverClientWithKey creates a client that sends API key headers when key is non-empty.
func NewEzSolverClientWithKey(baseURL string, timeoutSec int, apiKey string) *EzSolverClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultEzSolverURL
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	return &EzSolverClient{
		BaseURL: baseURL,
		APIKey:  strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{
			Timeout: time.Duration(timeoutSec+15) * time.Second,
		},
		TimeoutSec: timeoutSec,
	}
}

func (c *EzSolverClient) applyAuth(req *http.Request) {
	if c == nil || req == nil {
		return
	}
	k := strings.TrimSpace(c.APIKey)
	if k == "" {
		return
	}
	req.Header.Set("X-Api-Key", k)
	req.Header.Set("X-EzSolver-Token", k)
	req.Header.Set("Authorization", "Bearer "+k)
}

// HealthResult is returned by Health.
type HealthResult struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
	Latency int64  `json:"latency_ms,omitempty"`
}

// Health pings EzSolver root (or /health if present).
func (c *EzSolverClient) Health(ctx context.Context) HealthResult {
	if c == nil {
		return HealthResult{OK: false, Message: "ezsolver client nil"}
	}
	url := c.BaseURL + "/"
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HealthResult{OK: false, URL: c.BaseURL, Message: err.Error()}
	}
	c.applyAuth(req)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return HealthResult{OK: false, URL: c.BaseURL, Message: err.Error(), Latency: latency}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 500
	if !ok {
		return HealthResult{OK: false, URL: c.BaseURL, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, msg), Status: resp.StatusCode, Latency: latency}
	}
	if msg == "" {
		msg = "reachable"
	}
	return HealthResult{OK: true, URL: c.BaseURL, Message: msg, Status: resp.StatusCode, Latency: latency}
}

// SolveRequest is the body for POST /solve.
type SolveRequest struct {
	SiteKey string `json:"sitekey"`
	SiteURL string `json:"siteurl"`
	Timeout int    `json:"timeout,omitempty"`
}

// SolveResponse is the JSON from EzSolver.
type SolveResponse struct {
	Token   string `json:"token"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Solve obtains a Turnstile token via free EzSolver.
func (c *EzSolverClient) Solve(ctx context.Context, siteKey, siteURL string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("ezsolver client nil")
	}
	if strings.TrimSpace(siteKey) == "" {
		siteKey = GrokTurnstileSiteKey
	}
	payload, _ := json.Marshal(SolveRequest{
		SiteKey: siteKey,
		SiteURL: siteURL,
		Timeout: c.TimeoutSec,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/solve", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(c.TimeoutSec+15) * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ezsolver request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ezsolver HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var out SolveResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("ezsolver invalid json: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ezsolver: %s", out.Error)
	}
	if out.Message != "" && strings.TrimSpace(out.Token) == "" {
		return "", fmt.Errorf("ezsolver: %s", out.Message)
	}
	token := strings.TrimSpace(out.Token)
	if token == "" {
		return "", fmt.Errorf("ezsolver: empty token")
	}
	return token, nil
}

// SolverRecommendation is the solver's auto-tuned optimal config (from detected
// CPU/RAM), surfaced so the UI can show "推荐配置" and one-click apply.
type SolverRecommendation struct {
	Cores           int    `json:"cores"`
	MemMB           int    `json:"mem_mb"`
	Workers         int    `json:"workers"`
	WarmPool        bool   `json:"warm_pool"`
	PoolStandby     int    `json:"pool_standby"`
	ReusePerBrowser int    `json:"reuse_per_browser"`
	Reason          string `json:"reason"`
}

// WorkersSnapshot is returned by GET /health and POST /admin/workers|/admin/config.
type WorkersSnapshot struct {
	OK             bool    `json:"ok"`
	URL            string  `json:"url"`
	Status         string  `json:"status,omitempty"`
	Version        string  `json:"version,omitempty"`
	Workers        int     `json:"workers"`
	Ready          int     `json:"ready,omitempty"`
	BrowserBusy    int     `json:"browser_busy,omitempty"`
	Active         int     `json:"active"`
	Queued         int     `json:"queued"`
	Solved         int64   `json:"solved,omitempty"`
	Failed         int64   `json:"failed,omitempty"`
	AverageElapsed float64 `json:"average_elapsed,omitempty"`
	LastElapsed    float64 `json:"last_elapsed,omitempty"`
	Message        string  `json:"message,omitempty"`
	Applied        int     `json:"applied,omitempty"`

	// Warm-pool state (VeloraTurn Go solver). WarmPool==nil ⇒ solver is an older
	// build / EzSolver without pool support.
	WarmPool        *bool                 `json:"warm_pool,omitempty"`
	PoolSize        int                   `json:"pool_size,omitempty"`
	PoolStandby     int                   `json:"pool_standby,omitempty"`
	ReusePerBrowser int                   `json:"reuse_per_browser,omitempty"`
	Recommended     *SolverRecommendation `json:"recommended,omitempty"`
}

// solverStatusFields is the shared decode shape for /health and /admin/config.
type solverStatusFields struct {
	Status          string                `json:"status"`
	Version         string                `json:"version"`
	Workers         int                   `json:"workers"`
	Ready           int                   `json:"ready"`
	BrowserBusy     int                   `json:"browser_busy"`
	Active          int                   `json:"active"`
	Queued          int                   `json:"queued"`
	Solved          int64                 `json:"solved"`
	Failed          int64                 `json:"failed"`
	AverageElapsed  float64               `json:"average_elapsed"`
	LastElapsed     float64               `json:"last_elapsed"`
	WarmPool        *bool                 `json:"warm_pool"`
	PoolSize        int                   `json:"pool_size"`
	PoolStandby     int                   `json:"pool_standby"`
	ReusePerBrowser int                   `json:"reuse_per_browser"`
	Recommended     *SolverRecommendation `json:"recommended"`
	Applied         int                   `json:"applied"`
	Error           string                `json:"error"`
}

func (c *EzSolverClient) doAdminConfig(ctx context.Context, method string, payload []byte) WorkersSnapshot {
	if c == nil {
		return WorkersSnapshot{OK: false, Message: "ezsolver client nil"}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/admin/config", bodyReader)
	if err != nil {
		return WorkersSnapshot{OK: false, URL: base, Message: err.Error()}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyAuth(req)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return WorkersSnapshot{OK: false, URL: base, Message: err.Error() + "（旧版打码服务无 /admin/config，请升级 VeloraTurn Go 二进制）"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == 404 {
		return WorkersSnapshot{OK: false, URL: base, Message: "此打码服务不支持预热池配置（仅 VeloraTurn Go 支持）"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WorkersSnapshot{OK: false, URL: base, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))}
	}
	var out solverStatusFields
	_ = json.Unmarshal(body, &out)
	if out.Error != "" {
		return WorkersSnapshot{OK: false, URL: base, Message: out.Error}
	}
	return WorkersSnapshot{
		OK:              true,
		URL:             base,
		Status:          out.Status,
		Version:         out.Version,
		Workers:         out.Workers,
		Ready:           out.Ready,
		BrowserBusy:     out.BrowserBusy,
		Active:          out.Active,
		Queued:          out.Queued,
		Solved:          out.Solved,
		Failed:          out.Failed,
		AverageElapsed:  out.AverageElapsed,
		LastElapsed:     out.LastElapsed,
		Applied:         out.Applied,
		WarmPool:        out.WarmPool,
		PoolSize:        out.PoolSize,
		PoolStandby:     out.PoolStandby,
		ReusePerBrowser: out.ReusePerBrowser,
		Recommended:     out.Recommended,
	}
}

// Config reads the solver's live config + auto recommendation (GET /admin/config).
func (c *EzSolverClient) Config(ctx context.Context) WorkersSnapshot {
	return c.doAdminConfig(ctx, http.MethodGet, nil)
}

// PoolConfigInput carries warm-pool controls to push to the solver. Nil fields
// are omitted so a partial update only changes what the operator set.
type PoolConfigInput struct {
	WarmPool        *bool `json:"warm_pool,omitempty"`
	PoolStandby     *int  `json:"pool_standby,omitempty"`
	ReusePerBrowser *int  `json:"reuse_per_browser,omitempty"`
	Workers         *int  `json:"workers,omitempty"`
}

// SetConfig pushes warm-pool / worker settings live (POST /admin/config).
func (c *EzSolverClient) SetConfig(ctx context.Context, in PoolConfigInput) WorkersSnapshot {
	payload, _ := json.Marshal(in)
	return c.doAdminConfig(ctx, http.MethodPost, payload)
}

// SetWorkers resizes EzSolver MAX_WORKERS live (POST /admin/workers).
// Follows settings: set how many workers, no client-side hardcode.
func (c *EzSolverClient) SetWorkers(ctx context.Context, workers int) WorkersSnapshot {
	if c == nil {
		return WorkersSnapshot{OK: false, Message: "ezsolver client nil"}
	}
	if workers < 1 {
		workers = 1
	}
	// No upper clamp — follow caller / settings exactly.
	base := strings.TrimRight(c.BaseURL, "/")
	payload, _ := json.Marshal(map[string]int{"workers": workers, "max_workers": workers})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/admin/workers", bytes.NewReader(payload))
	if err != nil {
		return WorkersSnapshot{OK: false, URL: base, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return WorkersSnapshot{OK: false, URL: base, Message: err.Error() + "（旧版 EzSolver 无 /admin/workers，请升级 service.py 或本机一键安装）"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WorkersSnapshot{OK: false, URL: base, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))}
	}
	var out struct {
		Status         string  `json:"status"`
		Version        string  `json:"version"`
		Workers        int     `json:"workers"`
		Ready          int     `json:"ready"`
		BrowserBusy    int     `json:"browser_busy"`
		Active         int     `json:"active"`
		Queued         int     `json:"queued"`
		Solved         int64   `json:"solved"`
		Failed         int64   `json:"failed"`
		AverageElapsed float64 `json:"average_elapsed"`
		LastElapsed    float64 `json:"last_elapsed"`
		Applied        int     `json:"applied"`
		Error          string  `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Error != "" {
		return WorkersSnapshot{OK: false, URL: base, Message: out.Error}
	}
	w := out.Workers
	if out.Applied > 0 {
		w = out.Applied
	}
	return WorkersSnapshot{
		OK:             true,
		URL:            base,
		Status:         out.Status,
		Version:        out.Version,
		Workers:        w,
		Ready:          out.Ready,
		BrowserBusy:    out.BrowserBusy,
		Active:         out.Active,
		Queued:         out.Queued,
		Solved:         out.Solved,
		Failed:         out.Failed,
		AverageElapsed: out.AverageElapsed,
		LastElapsed:    out.LastElapsed,
		Applied:        out.Applied,
		Message:        fmt.Sprintf("workers=%d ready=%d active=%d queued=%d", w, out.Ready, out.Active, out.Queued),
	}
}

// ReleaseBrowsers asks the solver to drop all pooled Chrome instances now
// (POST /admin/release). Called when registration stops so idle browsers stop
// holding RAM instead of lingering until the next solve. Best-effort: an older
// solver without the endpoint returns 404 and this reports OK=false quietly.
func (c *EzSolverClient) ReleaseBrowsers(ctx context.Context) WorkersSnapshot {
	if c == nil {
		return WorkersSnapshot{OK: false, Message: "ezsolver client nil"}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/admin/release", bytes.NewReader([]byte("{}")))
	if err != nil {
		return WorkersSnapshot{OK: false, URL: base, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return WorkersSnapshot{OK: false, URL: base, Message: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == 404 {
		return WorkersSnapshot{OK: false, URL: base, Message: "旧版 EzSolver 无 /admin/release（升级 service.py 后支持停止即释放浏览器）"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WorkersSnapshot{OK: false, URL: base, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))}
	}
	var out struct {
		Released int    `json:"released"`
		Workers  int    `json:"workers"`
		Ready    int    `json:"ready"`
		Message  string `json:"message"`
	}
	_ = json.Unmarshal(body, &out)
	msg := out.Message
	if msg == "" {
		msg = fmt.Sprintf("released=%d ready=%d", out.Released, out.Ready)
	}
	return WorkersSnapshot{OK: true, URL: base, Workers: out.Workers, Ready: out.Ready, Message: msg}
}

// HealthDetail returns health with worker counts when the server exposes them.
func (c *EzSolverClient) HealthDetail(ctx context.Context) WorkersSnapshot {
	h := c.Health(ctx)
	snap := WorkersSnapshot{OK: h.OK, URL: h.URL, Message: h.Message}
	if !h.OK {
		return snap
	}
	// Parse workers/active/queued from health body if present in Message JSON-ish.
	// Prefer re-fetch /health as structured JSON.
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return snap
	}
	c.applyAuth(req)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return snap
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		Status         string  `json:"status"`
		Version        string  `json:"version"`
		Workers        int     `json:"workers"`
		Ready          int     `json:"ready"`
		BrowserBusy    int     `json:"browser_busy"`
		Active         int     `json:"active"`
		Queued         int     `json:"queued"`
		Solved         int64   `json:"solved"`
		Failed         int64   `json:"failed"`
		AverageElapsed float64 `json:"average_elapsed"`
		LastElapsed    float64 `json:"last_elapsed"`
	}
	if json.Unmarshal(body, &out) == nil {
		snap.Status = out.Status
		snap.Version = out.Version
		snap.Workers = out.Workers
		snap.Ready = out.Ready
		snap.BrowserBusy = out.BrowserBusy
		snap.Active = out.Active
		snap.Queued = out.Queued
		snap.Solved = out.Solved
		snap.Failed = out.Failed
		snap.AverageElapsed = out.AverageElapsed
		snap.LastElapsed = out.LastElapsed
		if out.Workers > 0 {
			snap.Message = fmt.Sprintf("workers=%d ready=%d active=%d queued=%d", out.Workers, out.Ready, out.Active, out.Queued)
		}
	}
	return snap
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
