package cfemail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "embed"
)

//go:embed cf-email-worker.js
var cfEmailWorkerScript []byte

const (
	cfAPIBase             = "https://api.cloudflare.com/client/v4"
	cfWorkerCompatDate    = "2024-09-23"
	cfDefaultWorkerPrefix = "kiro-email"
	cfDefaultKVTitle      = "kiro-otp-store"
)

// WorkerEntry is the EDU/CF worker config returned by provision (persisted by EduEmailService).
type WorkerEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Domain        string `json:"domain"`
	WorkerURL     string `json:"worker_url"`
	ApiKey        string `json:"api_key,omitempty"`
	CFAccountID   string `json:"cf_account_id,omitempty"`
	CFApiToken    string `json:"cf_api_token,omitempty"`
	KVNamespaceID string `json:"kv_namespace_id,omitempty"`
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func generateToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// generateHumanLikeUsername matches grokregister.generateHumanLikeUsername (same patterns/weights)
// so admin "generate addresses" and registration use the same local-part logic.
func generateHumanLikeUsername() string {
	firstNamesPool := []string{
		"james", "robert", "john", "michael", "david", "william", "richard", "joseph", "thomas", "chris",
		"mary", "patricia", "jennifer", "linda", "barbara", "elizabeth", "susan", "jessica", "sarah", "karen",
		"daniel", "matthew", "anthony", "mark", "steven", "andrew", "paul", "joshua", "kenneth", "brian",
		"kevin", "timothy", "jason", "jeffrey", "ryan", "jacob", "gary", "nicholas", "eric", "laura",
		"amanda", "melissa", "deborah", "stephanie", "rebecca", "sharon", "cynthia", "kathleen", "amy", "alex",
		"benjamin", "samuel", "nathan", "henry", "peter", "tyler", "dylan", "connor", "caleb", "rachel",
		"megan", "hannah", "victoria", "olivia", "samantha", "natalie", "grace", "sophia", "emma", "noah",
		"liam", "oliver", "charlotte", "amelia", "ava", "lucas", "mason", "ethan", "logan", "jack",
	}
	lastNamesPool := []string{
		"smith", "johnson", "williams", "brown", "jones", "garcia", "miller", "davis", "rodriguez", "martinez",
		"anderson", "taylor", "thomas", "moore", "martin", "jackson", "thompson", "white", "lopez", "lee",
		"harris", "clark", "lewis", "walker", "young", "allen", "king", "wright", "scott", "hill",
		"green", "adams", "baker", "nelson", "carter", "mitchell", "perez", "roberts", "turner", "campbell",
	}
	pattern := mathrand.Intn(100)
	first := firstNamesPool[mathrand.Intn(len(firstNamesPool))]
	last := lastNamesPool[mathrand.Intn(len(lastNamesPool))]
	year := 1975 + mathrand.Intn(31)
	month := 1 + mathrand.Intn(12)
	day := 1 + mathrand.Intn(28)
	dobFull := fmt.Sprintf("%04d%02d%02d", year, month, day)
	dobShort := fmt.Sprintf("%02d%02d%02d", year%100, month, day)
	switch {
	case pattern < 30:
		return first + last + dobFull
	case pattern < 50:
		return first + last + dobShort
	case pattern < 65:
		return first + dobFull
	case pattern < 80:
		return last + first + dobShort
	case pattern < 90:
		return first + last + fmt.Sprintf("%02d", mathrand.Intn(100))
	default:
		n := 4
		if len(last) < n {
			n = len(last)
		}
		return first + last[:n] + fmt.Sprintf("%04d", mathrand.Intn(10000))
	}
}

type cfAPIClient struct {
	accountID string
	token     string
	baseURL   string
	http      *http.Client
}

func newCFAPIClient(accountID, token string) *cfAPIClient {
	return &cfAPIClient{
		accountID: strings.TrimSpace(accountID),
		token:     strings.TrimSpace(token),
		baseURL:   cfAPIBase,
		http:      &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *cfAPIClient) apiBase() string {
	if c != nil && strings.TrimSpace(c.baseURL) != "" {
		return strings.TrimRight(c.baseURL, "/")
	}
	return cfAPIBase
}

type cfAPIEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (c *cfAPIClient) do(method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.apiBase()+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Cloudflare API 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env cfAPIEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 CF 响应失败 (HTTP %d): %s", resp.StatusCode, truncStr(string(raw), 200))
	}
	if !env.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(env.Errors) > 0 {
			parts := make([]string, 0, len(env.Errors))
			for _, e := range env.Errors {
				parts = append(parts, fmt.Sprintf("[%d] %s", e.Code, e.Message))
			}
			msg = strings.Join(parts, "; ")
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return env.Result, nil
}

func (c *cfAPIClient) doRaw(method, path string, contentType string, body io.Reader) (json.RawMessage, error) {
	req, err := http.NewRequest(method, c.apiBase()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Cloudflare API 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env cfAPIEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 CF 响应失败 (HTTP %d): %s", resp.StatusCode, truncStr(string(raw), 200))
	}
	if !env.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(env.Errors) > 0 {
			parts := make([]string, 0, len(env.Errors))
			for _, e := range env.Errors {
				parts = append(parts, fmt.Sprintf("[%d] %s", e.Code, e.Message))
			}
			msg = strings.Join(parts, "; ")
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return env.Result, nil
}

// ── Provision types ──

type ProvisionRequest struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	AccountID   string `json:"cf_account_id"`
	APIToken    string `json:"cf_api_token"`
	WorkerName  string `json:"worker_name,omitempty"`
	CallbackURL string `json:"callback_url,omitempty"`
	APIKey      string `json:"api_key,omitempty"`  // empty → auto generate
	SyncEDU     *bool  `json:"sync_edu,omitempty"` // default true
}

type ProvisionBatchRequest struct {
	Domains     []string `json:"domains"`
	AccountID   string   `json:"cf_account_id"`
	APIToken    string   `json:"cf_api_token"`
	CallbackURL string   `json:"callback_url,omitempty"`
	NamePrefix  string   `json:"name_prefix,omitempty"`
	SyncEDU     *bool    `json:"sync_edu,omitempty"`
}

type ProvisionStep struct {
	Step    string `json:"step"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

type ProvisionResult struct {
	OK      bool            `json:"ok"`
	Domain  string          `json:"domain"`
	Entry   *WorkerEntry    `json:"entry,omitempty"`
	Steps   []ProvisionStep `json:"steps"`
	Error   string          `json:"error,omitempty"`
	Worker  string          `json:"worker_name,omitempty"`
	KVTitle string          `json:"kv_title,omitempty"`
}

func sanitizeWorkerName(domain, preferred string) string {
	name := strings.TrimSpace(preferred)
	if name == "" {
		name = cfDefaultWorkerPrefix + "-" + domain
	}
	name = strings.ToLower(name)
	re := regexp.MustCompile(`[^a-z0-9-]`)
	name = re.ReplaceAllString(name, "-")
	name = regexp.MustCompile(`-+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > 50 {
		name = name[:50]
		name = strings.Trim(name, "-")
	}
	if name == "" {
		name = cfDefaultWorkerPrefix
	}
	return name
}

func normalizeDomain(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "https://")
	if i := strings.Index(d, "/"); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimPrefix(d, "www.")
	return strings.TrimSpace(d)
}

func (c *cfAPIClient) findZoneID(domain string) (zoneID string, zoneName string, err error) {
	// Exact match first
	result, err := c.do("GET", "/zones?name="+domain+"&status=active", nil)
	if err != nil {
		return "", "", fmt.Errorf("查询 Zone 失败: %w", err)
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(result, &zones); err != nil {
		return "", "", err
	}
	if len(zones) > 0 {
		return zones[0].ID, zones[0].Name, nil
	}
	// Parent zone for subdomains
	parts := strings.Split(domain, ".")
	for i := 1; i < len(parts)-1; i++ {
		parent := strings.Join(parts[i:], ".")
		result, err = c.do("GET", "/zones?name="+parent+"&status=active", nil)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(result, &zones); err != nil || len(zones) == 0 {
			continue
		}
		return zones[0].ID, zones[0].Name, nil
	}
	return "", "", fmt.Errorf("未找到域名 %s 对应的 Cloudflare Zone（请确认域名已托管到 CF，且 Token 有 Zone 读权限）", domain)
}

func (c *cfAPIClient) enableEmailRouting(zoneID string) error {
	// Prefer DNS enable endpoint (sets MX/SPF). Fall back to legacy /enable.
	if _, err := c.do("POST", "/zones/"+zoneID+"/email/routing/dns", map[string]any{}); err == nil {
		return nil
	} else {
		// already enabled is ok; try legacy
		if _, err2 := c.do("POST", "/zones/"+zoneID+"/email/routing/enable", map[string]any{}); err2 != nil {
			// Check current status
			if st, stErr := c.do("GET", "/zones/"+zoneID+"/email/routing", nil); stErr == nil {
				var settings struct {
					Enabled bool   `json:"enabled"`
					Status  string `json:"status"`
				}
				if json.Unmarshal(st, &settings) == nil && (settings.Enabled || settings.Status == "ready") {
					return nil
				}
			}
			combined := fmt.Sprintf("%v / %v", err, err2)
			if strings.Contains(combined, "10000") || strings.Contains(strings.ToLower(combined), "authentication") {
				return fmt.Errorf("开启 Email Routing 失败（Token 权限不足）: %s — 请在 API Token 中增加 Zone → Zone Settings:Edit、DNS:Edit、Email Routing Rules:Edit（建议再加 Email Routing Addresses:Edit），Zone Resources 包含目标域名后重建 Token", combined)
			}
			return fmt.Errorf("开启 Email Routing 失败: %s", combined)
		}
	}
	return nil
}

func (c *cfAPIClient) ensureKVNamespace(title string) (string, bool, error) {
	// List and reuse by title
	result, err := c.do("GET", "/accounts/"+c.accountID+"/storage/kv/namespaces?per_page=100", nil)
	if err == nil {
		var list []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if json.Unmarshal(result, &list) == nil {
			for _, ns := range list {
				if ns.Title == title {
					return ns.ID, true, nil
				}
			}
		}
	}
	result, err = c.do("POST", "/accounts/"+c.accountID+"/storage/kv/namespaces", map[string]string{"title": title})
	if err != nil {
		return "", false, fmt.Errorf("创建 KV 失败: %w", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &created); err != nil || created.ID == "" {
		return "", false, fmt.Errorf("创建 KV 响应异常")
	}
	return created.ID, false, nil
}

func (c *cfAPIClient) uploadEmailWorker(scriptName, kvNamespaceID string) error {
	meta := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": cfWorkerCompatDate,
		"bindings": []map[string]string{
			{
				"type":         "kv_namespace",
				"name":         "OTP_STORE",
				"namespace_id": kvNamespaceID,
			},
		},
	}
	metaJSON, _ := json.Marshal(meta)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// metadata part
	mh := textproto.MIMEHeader{}
	mh.Set("Content-Disposition", `form-data; name="metadata"`)
	mh.Set("Content-Type", "application/json")
	part, err := w.CreatePart(mh)
	if err != nil {
		return err
	}
	if _, err := part.Write(metaJSON); err != nil {
		return err
	}

	// worker module part
	sh := textproto.MIMEHeader{}
	sh.Set("Content-Disposition", `form-data; name="worker.js"; filename="worker.js"`)
	sh.Set("Content-Type", "application/javascript+module")
	sp, err := w.CreatePart(sh)
	if err != nil {
		return err
	}
	if _, err := sp.Write(cfEmailWorkerScript); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	path := "/accounts/" + c.accountID + "/workers/scripts/" + scriptName
	if _, err := c.doRaw("PUT", path, w.FormDataContentType(), &buf); err != nil {
		return fmt.Errorf("部署 Worker 失败: %w", err)
	}
	return nil
}

func (c *cfAPIClient) putWorkerSecret(scriptName, name, text string) error {
	_, err := c.do("PUT",
		"/accounts/"+c.accountID+"/workers/scripts/"+scriptName+"/secrets",
		map[string]string{
			"name": name,
			"text": text,
			"type": "secret_text",
		},
	)
	if err != nil {
		return fmt.Errorf("写入 Secret %s 失败: %w", name, err)
	}
	return nil
}

func (c *cfAPIClient) workersDevEnabled(scriptName string) (bool, error) {
	result, err := c.do("GET", "/accounts/"+c.accountID+"/workers/scripts/"+scriptName+"/subdomain", nil)
	if err != nil {
		return false, err
	}
	var st struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(result, &st); err != nil {
		return false, err
	}
	return st.Enabled, nil
}

func (c *cfAPIClient) enableWorkersDev(scriptName string) error {
	// Already enabled — keep previews on best-effort.
	if enabled, err := c.workersDevEnabled(scriptName); err == nil && enabled {
		_, _ = c.do("POST",
			"/accounts/"+c.accountID+"/workers/scripts/"+scriptName+"/subdomain",
			map[string]any{"enabled": true, "previews_enabled": true},
		)
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		_, err := c.do("POST",
			"/accounts/"+c.accountID+"/workers/scripts/"+scriptName+"/subdomain",
			map[string]any{"enabled": true, "previews_enabled": true},
		)
		if err == nil {
			if enabled, gerr := c.workersDevEnabled(scriptName); gerr == nil && enabled {
				return nil
			} else if gerr != nil {
				lastErr = gerr
			} else {
				lastErr = fmt.Errorf("subdomain API 返回成功但 enabled=false")
			}
		} else {
			lastErr = err
			// If POST fails but GET already reports enabled, treat as success.
			if enabled, gerr := c.workersDevEnabled(scriptName); gerr == nil && enabled {
				return nil
			}
		}
		if attempt < 4 {
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown error")
	}
	return fmt.Errorf("启用 workers.dev 失败: %w", lastErr)
}

func (c *cfAPIClient) workersDevSubdomain() (string, error) {
	result, err := c.do("GET", "/accounts/"+c.accountID+"/workers/subdomain", nil)
	if err != nil {
		return "", fmt.Errorf("获取 workers.dev 子域失败（请先在 CF 控制台打开一次 Workers，或检查 Token 权限）: %w", err)
	}
	var sub struct {
		Subdomain string `json:"subdomain"`
	}
	if err := json.Unmarshal(result, &sub); err != nil || sub.Subdomain == "" {
		return "", fmt.Errorf("账号尚未设置 workers.dev 子域，请打开 Cloudflare Dashboard → Workers 完成初始化")
	}
	return sub.Subdomain, nil
}

func (c *cfAPIClient) setCatchAllToWorker(zoneID, workerScriptName string) error {
	body := map[string]any{
		"actions": []map[string]any{
			{"type": "worker", "value": []string{workerScriptName}},
		},
		"matchers": []map[string]string{
			{"type": "all"},
		},
		"enabled": true,
		"name":    "KiroGo OTP catch-all",
	}
	if _, err := c.do("PUT", "/zones/"+zoneID+"/email/routing/rules/catch_all", body); err != nil {
		// Fallback: create a normal "all" rule
		if _, err2 := c.do("POST", "/zones/"+zoneID+"/email/routing/rules", body); err2 != nil {
			return fmt.Errorf("设置 Catch-all → Worker 失败: %v / %v", err, err2)
		}
	}
	return nil
}

func Provision(req ProvisionRequest) ProvisionResult {
	res := ProvisionResult{Domain: normalizeDomain(req.Domain), Steps: []ProvisionStep{}}
	add := func(step string, ok bool, detail string, skipped bool) {
		res.Steps = append(res.Steps, ProvisionStep{Step: step, OK: ok, Detail: detail, Skipped: skipped})
	}

	domain := res.Domain
	if domain == "" || !strings.Contains(domain, ".") {
		res.Error = "域名无效"
		return res
	}
	accountID := strings.TrimSpace(req.AccountID)
	token := strings.TrimSpace(req.APIToken)
	if accountID == "" || token == "" {
		res.Error = "需要 Cloudflare Account ID 与 API Token"
		return res
	}

	syncEDU := true
	if req.SyncEDU != nil {
		syncEDU = *req.SyncEDU
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = generateToken()[:24]
	}
	workerName := sanitizeWorkerName(domain, req.WorkerName)
	res.Worker = workerName
	kvTitle := cfDefaultKVTitle + "-" + strings.ReplaceAll(domain, ".", "-")
	if len(kvTitle) > 64 {
		kvTitle = kvTitle[:64]
	}
	res.KVTitle = kvTitle

	client := newCFAPIClient(accountID, token)

	// 1. Zone
	zoneID, zoneName, err := client.findZoneID(domain)
	if err != nil {
		add("查找 Zone", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("查找 Zone", true, fmt.Sprintf("%s (%s)", zoneName, zoneID), false)

	// 2. Email Routing
	if err := client.enableEmailRouting(zoneID); err != nil {
		add("开启 Email Routing", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("开启 Email Routing", true, "MX/SPF 已配置或已就绪", false)

	// 3. KV
	kvID, reused, err := client.ensureKVNamespace(kvTitle)
	if err != nil {
		add("创建/复用 KV", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	if reused {
		add("创建/复用 KV", true, "已存在: "+kvID, true)
	} else {
		add("创建/复用 KV", true, kvID, false)
	}

	// 4. Deploy worker
	if err := client.uploadEmailWorker(workerName, kvID); err != nil {
		add("部署 Email Worker", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("部署 Email Worker", true, workerName, false)

	// 5. Secrets
	if err := client.putWorkerSecret(workerName, "API_KEY", apiKey); err != nil {
		add("写入 API_KEY", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("写入 API_KEY", true, "已设置", false)

	cb := strings.TrimSpace(req.CallbackURL)
	if cb != "" {
		if err := client.putWorkerSecret(workerName, "CALLBACK_URL", cb); err != nil {
			add("写入 CALLBACK_URL", false, err.Error(), false)
			// non-fatal: KV poll still works
			log.Printf("[CF Provision] CALLBACK_URL 失败(非致命): %v", err)
		} else {
			add("写入 CALLBACK_URL", true, cb, false)
		}
	} else {
		add("写入 CALLBACK_URL", true, "未提供，将依赖 Worker/KV 轮询", true)
	}
	// CF secret deploy is async; brief head start before enable/health checks.
	time.Sleep(800 * time.Millisecond)

	// 6. workers.dev
	sub, err := client.workersDevSubdomain()
	if err != nil {
		add("获取 workers.dev 子域", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("获取 workers.dev 子域", true, sub+".workers.dev", false)

	if err := client.enableWorkersDev(workerName); err != nil {
		add("启用 Worker 的 workers.dev", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("启用 Worker 的 workers.dev", true, "enabled", false)

	workerURL := fmt.Sprintf("https://%s.%s.workers.dev", workerName, sub)

	// 7. Catch-all → worker
	if err := client.setCatchAllToWorker(zoneID, workerName); err != nil {
		add("Catch-all → Worker", false, err.Error(), false)
		res.Error = err.Error()
		return res
	}
	add("Catch-all → Worker", true, "*@"+domain+" → "+workerName, false)

	// 8. Health check with retries. 1042 usually means workers.dev route not ready yet.
	healthOK := false
	healthDetail := ""
	{
		cl := &http.Client{Timeout: 10 * time.Second}
		hURL := workerURL + "/api/health?key=" + url.QueryEscape(apiKey)
		for attempt := 1; attempt <= 12; attempt++ {
			// Re-assert workers.dev on early failures; CF sometimes lags after first enable.
			if attempt == 4 || attempt == 8 {
				if err := client.enableWorkersDev(workerName); err != nil {
					log.Printf("[CF Provision] re-enable workers.dev: %v", err)
				}
			}
			// Secret put is async; rewrite once mid-loop if still unauthorized.
			if attempt == 6 {
				if err := client.putWorkerSecret(workerName, "API_KEY", apiKey); err != nil {
					log.Printf("[CF Provision] re-put API_KEY: %v", err)
				}
			}
			hreq, _ := http.NewRequest("GET", hURL, nil)
			resp, herr := cl.Do(hreq)
			if herr != nil {
				healthDetail = herr.Error()
			} else {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					healthOK = true
					healthDetail = string(body)
					break
				}
				healthDetail = fmt.Sprintf("HTTP %d %s", resp.StatusCode, truncStr(string(body), 80))
				// Secret propagation commonly lags 3-8s after putWorkerSecret; keep retrying 401.
			}
			if attempt < 12 {
				time.Sleep(time.Duration(attempt) * 450 * time.Millisecond)
			}
		}
	}
	add("Worker 健康检查", healthOK, truncStr(healthDetail, 120), false)
	if !healthOK {
		hint := healthDetail
		low := strings.ToLower(healthDetail)
		if strings.Contains(healthDetail, "1042") || strings.Contains(low, "worker not found") {
			hint = healthDetail + " — workers.dev 路由未就绪/未启用。请确认 Token 含 Account → Workers Scripts:Edit，并在 CF Dashboard 打开过 Workers"
		} else if strings.Contains(healthDetail, "401") {
			hint = healthDetail + " — Worker 已可达但 API_KEY 尚未生效或密钥不匹配，可稍后重试开通"
		}
		res.Error = "Worker 健康检查失败: " + hint
		// Still return partial entry so UI can inspect worker_url; do not mark OK.
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = domain
		}
		entry := WorkerEntry{
			ID:            generateToken()[:12],
			Name:          name,
			Domain:        domain,
			WorkerURL:     workerURL,
			ApiKey:        apiKey,
			CFAccountID:   accountID,
			CFApiToken:    token,
			KVNamespaceID: kvID,
		}
		res.Entry = &entry
		return res
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = domain
	}

	entry := WorkerEntry{
		ID:            generateToken()[:12],
		Name:          name,
		Domain:        domain,
		WorkerURL:     workerURL,
		ApiKey:        apiKey,
		CFAccountID:   accountID,
		CFApiToken:    token,
		KVNamespaceID: kvID,
	}

	// Persistence is handled by EduEmailService (caller). Mark step for UI parity.
	if syncEDU {
		add("准备同步到 EDU 配置", true, entry.ID, false)
		log.Printf("[CF Provision] ready for EDU sync: %s @%s → %s", entry.Name, entry.Domain, entry.WorkerURL)
	} else {
		add("同步到 EDU 配置", true, "已跳过", true)
	}

	res.OK = true
	res.Entry = &entry
	return res
}

// ZoneInfo is one CF zone with optional Email Routing status.
type ZoneInfo struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Status              string `json:"status,omitempty"`
	EmailRoutingEnabled bool   `json:"email_routing_enabled"`
	EmailRoutingStatus  string `json:"email_routing_status,omitempty"` // ready | uninitialized | ...
	HasCatchAll         bool   `json:"has_catch_all"`
	CatchAllWorker      string `json:"catch_all_worker,omitempty"`
	ProbeError          string `json:"probe_error,omitempty"`
}

// ListZones lists active Cloudflare zones for the account (no routing probe).
func ListZones(accountID, token string) ([]map[string]string, error) {
	infos, err := ListZonesWithEmailStatus(accountID, token, false)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, len(infos))
	for _, z := range infos {
		out = append(out, map[string]string{"id": z.ID, "name": z.Name, "status": z.Status})
	}
	return out, nil
}

// ListZonesWithEmailStatus lists active zones and optionally probes Email Routing per zone.
// probeRouting=true hits GET /zones/{id}/email/routing (+ catch_all) with limited concurrency.
func ListZonesWithEmailStatus(accountID, token string, probeRouting bool) ([]ZoneInfo, error) {
	client := newCFAPIClient(accountID, token)
	out := make([]ZoneInfo, 0, 50)
	for page := 1; page <= 20; page++ {
		path := fmt.Sprintf("/zones?account.id=%s&per_page=50&page=%d&status=active", url.QueryEscape(strings.TrimSpace(accountID)), page)
		result, err := client.do("GET", path, nil)
		if err != nil {
			return nil, fmt.Errorf("列出 Zone 失败: %w", err)
		}
		var zones []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(result, &zones); err != nil {
			return nil, err
		}
		for _, z := range zones {
			out = append(out, ZoneInfo{ID: z.ID, Name: z.Name, Status: z.Status})
		}
		if len(zones) < 50 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if !probeRouting || len(out) == 0 {
		return out, nil
	}
	// Probe routing status (bounded parallelism).
	const workers = 8
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range out {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			en, st, hasCA, worker, perr := client.probeEmailRouting(out[i].ID)
			mu.Lock()
			out[i].EmailRoutingEnabled = en
			out[i].EmailRoutingStatus = st
			out[i].HasCatchAll = hasCA
			out[i].CatchAllWorker = worker
			out[i].ProbeError = perr
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}

// probeEmailRouting returns enabled, status, hasCatchAll, catchAllWorkerName, probeErr.
func (c *cfAPIClient) probeEmailRouting(zoneID string) (enabled bool, status string, hasCatchAll bool, catchAllWorker string, probeErr string) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return false, "", false, "", ""
	}
	result, err := c.do("GET", "/zones/"+zoneID+"/email/routing", nil)
	if err != nil {
		// Permission error or API failure — distinguish from confirmed "not open".
		return false, "unknown", false, "", truncStr(err.Error(), 120)
	}
	var st struct {
		Enabled bool   `json:"enabled"`
		Status  string `json:"status"`
		Name    string `json:"name"`
	}
	_ = json.Unmarshal(result, &st)
	enabled = st.Enabled || strings.EqualFold(st.Status, "ready")
	status = st.Status
	if status == "" && st.Enabled {
		status = "enabled"
	}
	// Catch-all rule (optional detail).
	if ca, err := c.do("GET", "/zones/"+zoneID+"/email/routing/rules/catch_all", nil); err == nil {
		var rule struct {
			Enabled bool `json:"enabled"`
			Actions []struct {
				Type  string   `json:"type"`
				Value []string `json:"value"`
			} `json:"actions"`
		}
		if json.Unmarshal(ca, &rule) == nil {
			hasCatchAll = rule.Enabled
			for _, a := range rule.Actions {
				if strings.EqualFold(a.Type, "worker") && len(a.Value) > 0 {
					catchAllWorker = a.Value[0]
					hasCatchAll = true
					break
				}
			}
		}
	}
	return enabled, status, hasCatchAll, catchAllWorker, ""
}


// ImportOpenRequest imports already-provisioned CF Email Routing domains into the local EDU pool
// without re-running full provision (no re-upload script / re-enable routing unless missing pieces).
type ImportOpenRequest struct {
	Domain      string `json:"domain"`
	Name        string `json:"name,omitempty"`
	AccountID   string `json:"cf_account_id"`
	APIToken    string `json:"cf_api_token"`
	CallbackURL string `json:"callback_url,omitempty"`
	// WorkerName optional override; empty → use catch-all worker name from zone probe
	WorkerName string `json:"worker_name,omitempty"`
	// APIKey optional; empty → generate new and write Worker secret API_KEY
	APIKey string `json:"api_key,omitempty"`
}

// ImportOpenResult is one import attempt.
type ImportOpenResult struct {
	OK     bool            `json:"ok"`
	Domain string          `json:"domain"`
	Entry  *WorkerEntry    `json:"entry,omitempty"`
	Steps  []ProvisionStep `json:"steps"`
	Error  string          `json:"error,omitempty"`
}

// ImportOpen discovers an existing catch-all Worker + workers.dev URL, rotates API_KEY
// (CF secrets are write-only), optionally binds known KV, and returns a WorkerEntry for the EDU pool.
func ImportOpen(req ImportOpenRequest) ImportOpenResult {
	res := ImportOpenResult{Domain: normalizeDomain(req.Domain), Steps: []ProvisionStep{}}
	add := func(step string, ok bool, detail string, skipped bool) {
		res.Steps = append(res.Steps, ProvisionStep{Step: step, OK: ok, Detail: detail, Skipped: skipped})
	}
	domain := res.Domain
	if domain == "" || !strings.Contains(domain, ".") {
		res.Error = "域名无效"
		return res
	}
	accountID := strings.TrimSpace(req.AccountID)
	token := strings.TrimSpace(req.APIToken)
	if accountID == "" || token == "" {
		res.Error = "需要 Cloudflare Account ID 与 API Token"
		return res
	}
	client := newCFAPIClient(accountID, token)

	zoneID, zoneName, err := client.findZoneID(domain)
	if err != nil {
		res.Error = err.Error()
		add("查找 Zone", false, err.Error(), false)
		return res
	}
	add("查找 Zone", true, zoneName+" / "+zoneID, false)

	// Probe catch-all worker
	_, _, hasCA, catchWorker, perr := client.probeEmailRouting(zoneID)
	if perr != "" {
		add("探测 Email Routing", false, perr, false)
	} else {
		add("探测 Email Routing", true, fmt.Sprintf("catch_all=%v worker=%s", hasCA, catchWorker), false)
	}
	workerName := strings.TrimSpace(req.WorkerName)
	if workerName == "" {
		workerName = strings.TrimSpace(catchWorker)
	}
	if workerName == "" {
		// fallback naming convention used by Provision
		workerName = sanitizeWorkerName(domain, "")
		add("Worker 名称", true, "未探测到 catch-all，尝试约定名 "+workerName, true)
	} else {
		add("Worker 名称", true, workerName, false)
	}

	// Ensure workers.dev enabled and resolve public URL
	_ = client.enableWorkersDev(workerName)
	sub, err := client.workersDevSubdomain()
	if err != nil {
		res.Error = err.Error()
		add("workers.dev 子域", false, err.Error(), false)
		return res
	}
	workerURL := fmt.Sprintf("https://%s.%s.workers.dev", workerName, sub)
	add("Worker URL", true, workerURL, false)

	// KV namespace: read bindings if possible, else ensure titled namespace
	kvID := client.workerKVNamespaceID(workerName)
	if kvID != "" {
		add("KV 绑定", true, kvID, false)
	} else {
		kvTitle := cfDefaultKVTitle + "-" + strings.ReplaceAll(domain, ".", "-")
		if len(kvTitle) > 64 {
			kvTitle = kvTitle[:64]
		}
		id, reused, kerr := client.ensureKVNamespace(kvTitle)
		if kerr != nil {
			add("KV", false, kerr.Error(), false)
			// non-fatal for pool import if worker OTP via HTTP works with API_KEY only
		} else {
			kvID = id
			add("KV", true, kvID+map[bool]string{true: " (复用)", false: " (新建)"}[reused], false)
			// re-bind by light re-upload? skip full upload; OTP via worker HTTP + new API_KEY is enough
		}
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = generateToken()[:24]
	}
	if err := client.putWorkerSecret(workerName, "API_KEY", apiKey); err != nil {
		res.Error = "写入 API_KEY 失败: " + err.Error()
		add("同步 API_KEY", false, err.Error(), false)
		return res
	}
	add("同步 API_KEY", true, "已写入 Worker Secret（本地保存明文用于注册）", false)

	if cb := strings.TrimSpace(req.CallbackURL); cb != "" {
		if err := client.putWorkerSecret(workerName, "CALLBACK_URL", cb); err != nil {
			add("写入 CALLBACK_URL", false, err.Error(), true)
		} else {
			add("写入 CALLBACK_URL", true, cb, false)
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = domain
	}
	entry := WorkerEntry{
		ID:            "", // filled by EduService Upsert
		Name:          name,
		Domain:        domain,
		WorkerURL:     workerURL,
		ApiKey:        apiKey,
		CFAccountID:   accountID,
		CFApiToken:    token,
		KVNamespaceID: kvID,
	}
	res.OK = true
	res.Entry = &entry
	add("准备写入 EDU 池", true, domain, false)
	return res
}

// workerKVNamespaceID tries to read existing kv_namespace binding from Worker settings.
func (c *cfAPIClient) workerKVNamespaceID(scriptName string) string {
	scriptName = strings.TrimSpace(scriptName)
	if scriptName == "" {
		return ""
	}
	// GET settings (bindings)
	result, err := c.do("GET", "/accounts/"+c.accountID+"/workers/scripts/"+scriptName+"/settings", nil)
	if err != nil {
		// older API: bindings endpoint
		result, err = c.do("GET", "/accounts/"+c.accountID+"/workers/scripts/"+scriptName+"/bindings", nil)
		if err != nil {
			return ""
		}
	}
	// settings shape: { bindings: [ { type, name, namespace_id } ] }
	var settings struct {
		Bindings []struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			NamespaceID string `json:"namespace_id"`
		} `json:"bindings"`
	}
	if json.Unmarshal(result, &settings) == nil {
		for _, b := range settings.Bindings {
			if strings.EqualFold(b.Type, "kv_namespace") && b.NamespaceID != "" {
				return b.NamespaceID
			}
		}
	}
	// bindings array root
	var arr []struct {
		Type        string `json:"type"`
		NamespaceID string `json:"namespace_id"`
	}
	if json.Unmarshal(result, &arr) == nil {
		for _, b := range arr {
			if strings.EqualFold(b.Type, "kv_namespace") && b.NamespaceID != "" {
				return b.NamespaceID
			}
		}
	}
	return ""
}


// GenerateAddresses builds N catch-all style addresses for a domain (no CF API).
// When useEduSubdomain is true, addresses are {user}@edu.{domain} (unless domain already starts with edu.).
func GenerateAddresses(domain string, n int, useEduSubdomain bool) []string {
	domain = normalizeDomain(domain)
	if n <= 0 {
		n = 1
	}
	if n > 500 {
		n = 500
	}
	mailHost := domain
	if useEduSubdomain && domain != "" && !strings.HasPrefix(strings.ToLower(domain), "edu.") {
		mailHost = "edu." + domain
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = generateHumanLikeUsername() + "@" + mailHost
	}
	return out
}

// ProvisionBatch opens multiple domains sequentially.
func ProvisionBatch(req ProvisionBatchRequest) map[string]any {
	var domains []string
	seen := map[string]bool{}
	for _, d := range req.Domains {
		nd := normalizeDomain(d)
		if nd == "" || seen[nd] {
			continue
		}
		seen[nd] = true
		domains = append(domains, nd)
	}
	prefix := strings.TrimSpace(req.NamePrefix)
	results := make([]ProvisionResult, 0, len(domains))
	okCount := 0
	for _, d := range domains {
		name := d
		if prefix != "" {
			name = prefix + " " + d
		}
		one := Provision(ProvisionRequest{
			Name: name, Domain: d, AccountID: req.AccountID, APIToken: req.APIToken,
			CallbackURL: req.CallbackURL, SyncEDU: req.SyncEDU,
		})
		if one.OK {
			okCount++
		}
		results = append(results, one)
	}
	return map[string]any{
		"ok":      okCount == len(domains) && len(domains) > 0,
		"total":   len(domains),
		"success": okCount,
		"fail":    len(domains) - okCount,
		"results": results,
	}
}
