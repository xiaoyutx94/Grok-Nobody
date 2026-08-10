package grokregister

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// iCloudAPIEmail 接入 iCloud Privacy Mail 取码平台
// （https://github.com/q1953258942/iCloud-Privacy-Mail）：
//
//	自动取号：POST /api/v1/mailboxes/claim   （Authorization: Bearer <全局 api_key>）
//	取码轮询：GET  /api/v1/mailboxes/{email}/code?key=<mailbox_key>&after=<RFC3339>&keyword=<project>&wait_ms=12000
//
// 第一阶段只做外部 API 接入（与 web 版「icloud_api」接入设计一致）：
// 不直接实现 Apple/iCloud 登录与 Hide My Email 创建，只消费平台的
// 取号/取码接口。claim 成功即邮箱被标记 used，等待验证码期间轮询取码。
type iCloudAPIEmail struct {
	baseURL   string
	apiKey    string
	project   string
	client    *http.Client
	email     string
	apiURL    string // claim 返回的完整取码地址（含 mailbox_key）
	after     time.Time
	cancel    *SafeCancel
	logPrefix string
}

// NewICloudAPIEmail 构造 iCloud 取码平台接入（baseURL 如 http://127.0.0.1:8787）。
func NewICloudAPIEmail(baseURL, apiKey, project string) *iCloudAPIEmail {
	return &iCloudAPIEmail{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		project: strings.TrimSpace(project),
		client:  &http.Client{Timeout: 35 * time.Second},
		after:   time.Now(),
	}
}

func (e *iCloudAPIEmail) SetCancel(ch *SafeCancel)   { e.cancel = ch }
func (e *iCloudAPIEmail) SetLogPrefix(p string)      { e.logPrefix = p }
func (e *iCloudAPIEmail) SetCodeExtractor(func(string) string) {}

func (e *iCloudAPIEmail) Address() string { return e.email }

// APIURL 返回 claim 得到的完整取码地址（含 mailbox_key），供外部复用。
func (e *iCloudAPIEmail) APIURL() string { return e.apiURL }

// FetchICloudCode 静态取码：GET {apiURL}?after=&keyword=&wait_ms=。
// 供池模式（engine.iCloudPoolEmail）复用同一套平台协议解析。
func FetchICloudCode(apiURL, apiKey, after, project string) (string, error) {
	u := apiURL
	if u == "" {
		return "", fmt.Errorf("取码地址为空")
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += fmt.Sprintf("%safter=%s&keyword=%s&wait_ms=12000",
		sep, url.QueryEscape(after), url.QueryEscape(project))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("iCloud 取码请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("iCloud 取码响应解析失败: %w", err)
	}
	if out.Success && strings.TrimSpace(out.Code) != "" {
		return strings.TrimSpace(out.Code), nil
	}
	if out.Error != nil {
		switch out.Error.Code {
		case "no_code", "not_yet", "mail_not_received":
			return "", nil // 正常未到，继续轮询
		default:
			return "", fmt.Errorf("iCloud 取码: %s", out.Error.Message)
		}
	}
	return "", nil
}

// Create 自动取号：POST /api/v1/mailboxes/claim。返回邮箱地址。
func (e *iCloudAPIEmail) Create() (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"project": e.project,
		"purpose": "register",
		"count":   1,
	})
	req, err := http.NewRequest(http.MethodPost, e.baseURL+"/api/v1/mailboxes/claim", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("iCloud 取号请求构造失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("iCloud 取号请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Success bool `json:"success"`
		Mailbox struct {
			Email  string `json:"email"`
			APIURL string `json:"api_url"`
		} `json:"mailbox"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("iCloud 取号响应解析失败: %w", err)
	}
	if !out.Success || strings.TrimSpace(out.Mailbox.Email) == "" {
		msg := "iCloud 取号失败"
		if out.Error != nil && strings.TrimSpace(out.Error.Message) != "" {
			msg += ": " + out.Error.Message
		}
		return "", fmt.Errorf("%s", msg)
	}
	e.email = strings.TrimSpace(out.Mailbox.Email)
	e.apiURL = strings.TrimSpace(out.Mailbox.APIURL)
	e.after = time.Now()
	return e.email, nil
}

// WaitForCode 轮询取码接口直到拿到验证码或超时。
func (e *iCloudAPIEmail) WaitForCode(timeout, interval time.Duration) (string, error) {
	if e.email == "" {
		return "", fmt.Errorf("iCloud 邮箱未创建")
	}
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	lastErr := ""
	for time.Now().Before(deadline) {
		if isCancelled(e.cancel) {
			return "", fmt.Errorf("cancelled")
		}
		code, err := e.fetchCode()
		if err == nil && code != "" {
			return code, nil
		}
		if err != nil {
			lastErr = err.Error()
		}
		time.Sleep(interval)
	}
	if lastErr != "" {
		return "", fmt.Errorf("iCloud 等待验证码超时 (%s): %s", timeout.String(), lastErr)
	}
	return "", fmt.Errorf("iCloud 等待验证码超时 (%s) — 平台未收到邮件", timeout.String())
}

// fetchCode 单次取码。平台在本地缓存未命中时会阻塞等待后台 IMAP 补抓
// （wait_ms 控制，最大 30000），所以这里每次请求天然带等待窗口。
func (e *iCloudAPIEmail) fetchCode() (string, error) {
	u := e.apiURL
	if u == "" {
		u = fmt.Sprintf("%s/api/v1/mailboxes/%s/code?key=%s",
			e.baseURL, url.PathEscape(e.email), url.QueryEscape(e.apiKey))
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += fmt.Sprintf("%safter=%s&keyword=%s&wait_ms=12000",
		sep, url.QueryEscape(e.after.Format(time.RFC3339)), url.QueryEscape(e.project))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("iCloud 取码请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("iCloud 取码响应解析失败: %w", err)
	}
	if out.Success && strings.TrimSpace(out.Code) != "" {
		return strings.TrimSpace(out.Code), nil
	}
	if out.Error != nil {
		// no_code / not_yet 都是正常未到，不算错误，继续轮询
		switch out.Error.Code {
		case "no_code", "not_yet", "mail_not_received":
			return "", nil
		default:
			return "", fmt.Errorf("iCloud 取码: %s", out.Error.Message)
		}
	}
	return "", nil
}

// ResetForRetry 失败重试时重新取号（claim 的邮箱已标记 used）。
func (e *iCloudAPIEmail) ResetForRetry() error {
	e.email = ""
	e.apiURL = ""
	e.after = time.Now()
	return nil
}
