// Package captcha provides captcha provider clients used by Grok register.
package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultAuralithURL = "http://172.18.0.1:8194"

type AuralithClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	TimeoutSec int
}

type AuralithPhaseMetric struct {
	Count  int64   `json:"count"`
	Failed int64   `json:"failed"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
}
type AuralithMetrics struct {
	Active int64                          `json:"active"`
	Queued int64                          `json:"queued"`
	Solved int64                          `json:"solved"`
	Failed int64                          `json:"failed"`
	Phases map[string]AuralithPhaseMetric `json:"phases"`
}
type AuralithResource struct {
	RSSMB      int     `json:"rss_mb"`
	GoHeapMB   int     `json:"go_heap_mb"`
	CPUPercent float64 `json:"cpu_percent"`
}
type AuralithContextPool struct {
	Mode                string `json:"mode"`
	ExperimentalEnabled bool   `json:"experimental_enabled"`
	ProcessTarget       int    `json:"process_target"`
	ContextsPerProcess  int    `json:"contexts_per_process"`
	MaxUses             int    `json:"max_uses"`
	MaxAgeSec           int    `json:"max_age_sec"`
	LiveProcesses       int64  `json:"live_processes"`
	ReadyProcesses      int    `json:"ready_processes"`
	ActiveContexts      int64  `json:"active_contexts"`
	Fallbacks           int64  `json:"fallbacks"`
	CircuitOpen         bool   `json:"circuit_open"`
	CircuitUntil        string `json:"circuit_until"`
}
type AuralithBudget struct {
	Mode                  string  `json:"mode"`
	MaxBrowsers           int     `json:"max_browsers"`
	MaxContextsPerBrowser int     `json:"max_contexts_per_browser"`
	CPUHighWater          float64 `json:"cpu_high_water"`
	RSSHighWaterMB        int     `json:"rss_high_water_mb"`
	QueueTimeout          int64   `json:"queue_timeout"`
}
type AuralithSnapshot struct {
	OK             bool                       `json:"ok"`
	URL            string                     `json:"url"`
	Status         string                     `json:"status,omitempty"`
	Version        string                     `json:"version,omitempty"`
	Message        string                     `json:"message,omitempty"`
	BrowserReady   bool                       `json:"browser_ready"`
	ContextReady   bool                       `json:"context_ready"`
	OOPIFReady     bool                       `json:"oopif_ready"`
	ProxyReady     bool                       `json:"proxy_ready"`
	SolveReady     bool                       `json:"solve_ready"`
	TestMode       bool                       `json:"test_mode"`
	Workers        int                        `json:"workers,omitempty"`
	Ready          int                        `json:"ready,omitempty"`
	Active         int                        `json:"active,omitempty"`
	Queued         int                        `json:"queued,omitempty"`
	Solved         int64                      `json:"solved,omitempty"`
	Failed         int64                      `json:"failed,omitempty"`
	AverageElapsed float64                    `json:"average_elapsed,omitempty"`
	LastElapsed    float64                    `json:"last_elapsed,omitempty"`
	Released       int                        `json:"released,omitempty"`
	Metrics        AuralithMetrics            `json:"metrics"`
	Resource       AuralithResource           `json:"resource"`
	ContextPool    AuralithContextPool        `json:"context_pool"`
	Budget         AuralithBudget             `json:"budget"`
	RSSMB          int                        `json:"rss_mb,omitempty"`
	CPUPercent     float64                    `json:"cpu_percent,omitempty"`
	LatencyP50MS   float64                    `json:"latency_p50_ms,omitempty"`
	LatencyP95MS   float64                    `json:"latency_p95_ms,omitempty"`
	Raw            map[string]json.RawMessage `json:"-"`
}

func validateAuralithBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultAuralithURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid Auralith URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("Auralith URL scheme must be http or https")
	}
	if u.User != nil {
		return "", fmt.Errorf("Auralith URL credentials are not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Auralith URL query/fragment is not allowed")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil && !strings.EqualFold(host, "localhost") && u.Scheme != "https" {
		return "", fmt.Errorf("public Auralith hostnames require https")
	}
	if ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return "", fmt.Errorf("Auralith URL address is not allowed")
		}
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return "", fmt.Errorf("public Auralith IP literals are not allowed")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}
func NewAuralithClientValidated(baseURL string, timeoutSec int, apiKey string) (*AuralithClient, error) {
	base, err := validateAuralithBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return newAuralithClient(base, timeoutSec, apiKey), nil
}
func newAuralithClient(baseURL string, timeoutSec int, apiKey string) *AuralithClient {
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	return &AuralithClient{BaseURL: baseURL, APIKey: strings.TrimSpace(apiKey), TimeoutSec: timeoutSec, HTTPClient: &http.Client{Timeout: time.Duration(timeoutSec+15) * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

func NewAuralithClient(baseURL string, timeoutSec int, apiKey string) *AuralithClient {
	c, err := NewAuralithClientValidated(baseURL, timeoutSec, apiKey)
	if err != nil {
		return &AuralithClient{BaseURL: strings.TrimSpace(baseURL), APIKey: strings.TrimSpace(apiKey), TimeoutSec: timeoutSec, HTTPClient: &http.Client{Timeout: time.Second}}
	}
	return c
}

func (c *AuralithClient) applyAuth(req *http.Request) {
	if c == nil || req == nil || strings.TrimSpace(c.APIKey) == "" {
		return
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
}

func (c *AuralithClient) do(ctx context.Context, method, path string, payload any) AuralithSnapshot {
	if c == nil {
		return AuralithSnapshot{Message: "auralith client nil"}
	}
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return AuralithSnapshot{URL: c.BaseURL, Message: err.Error()}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyAuth(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return AuralithSnapshot{URL: c.BaseURL, Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := AuralithSnapshot{URL: c.BaseURL}
	_ = json.Unmarshal(raw, &out)
	_ = json.Unmarshal(raw, &out.Raw)
	out.RSSMB = out.Resource.RSSMB
	out.CPUPercent = out.Resource.CPUPercent
	if phase, ok := out.Metrics.Phases["solve"]; ok {
		out.LatencyP50MS = phase.P50MS
		out.LatencyP95MS = phase.P95MS
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.OK = false
		out.Message = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
		return out
	}
	out.OK = out.SolveReady || (path == "/admin/release" && out.OK)
	if !out.OK && out.Message == "" {
		out.Message = "Auralith not solve-ready"
	}
	return out
}

func (c *AuralithClient) Health(ctx context.Context) AuralithSnapshot {
	return c.do(ctx, http.MethodGet, "/health", nil)
}
func (c *AuralithClient) Config(ctx context.Context) AuralithSnapshot {
	return c.do(ctx, http.MethodGet, "/admin/config", nil)
}
func (c *AuralithClient) Release(ctx context.Context) AuralithSnapshot {
	return c.do(ctx, http.MethodPost, "/admin/release", map[string]any{"wait": false})
}
func (c *AuralithClient) SetConfig(ctx context.Context, payload map[string]any) AuralithSnapshot {
	return c.do(ctx, http.MethodPost, "/admin/config", payload)
}

// SetWorkers resizes the live Auralith worker gate (POST /admin/workers).
func (c *AuralithClient) SetWorkers(ctx context.Context, workers int) AuralithSnapshot {
	if workers < 1 {
		workers = 1
	}
	return c.do(ctx, http.MethodPost, "/admin/workers", map[string]any{"workers": workers})
}

func (c *AuralithClient) Solve(ctx context.Context, siteKey, siteURL, proxyURL string) (string, error) {
	payload := map[string]any{"sitekey": siteKey, "siteurl": siteURL, "websiteKey": siteKey, "websiteURL": siteURL, "timeout": c.TimeoutSec}
	if strings.TrimSpace(proxyURL) != "" {
		payload["proxy"] = strings.TrimSpace(proxyURL)
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/solve", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auralith request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("auralith HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var out struct {
		Token    string `json:"token"`
		Error    string `json:"error"`
		Solution struct {
			Token string `json:"token"`
		} `json:"solution"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("auralith invalid json: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("auralith: %s", out.Error)
	}
	tok := strings.TrimSpace(out.Token)
	if tok == "" {
		tok = strings.TrimSpace(out.Solution.Token)
	}
	if tok == "" {
		return "", fmt.Errorf("auralith: empty token")
	}
	return tok, nil
}
