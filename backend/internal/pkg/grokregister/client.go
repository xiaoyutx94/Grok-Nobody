package grokregister

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	azuretls "github.com/Noooste/azuretls-client"
	fhttp "github.com/Noooste/fhttp"
)

// OrderedHeaders represents HTTP headers in a specific order.
// This is critical for browser fingerprint consistency as anti-bot systems
// detect header ordering mismatches between claimed browser and actual requests.
type OrderedHeaders [][]string

// Set adds or updates a header in the ordered list.
func (oh *OrderedHeaders) Set(key, value string) {
	for i, h := range *oh {
		if len(h) >= 1 && strings.EqualFold(h[0], key) {
			(*oh)[i] = []string{key, value}
			return
		}
	}
	*oh = append(*oh, []string{key, value})
}

// Get returns the value for a header key.
func (oh OrderedHeaders) Get(key string) string {
	for _, h := range oh {
		if len(h) >= 2 && strings.EqualFold(h[0], key) {
			return h[1]
		}
	}
	return ""
}

// Has checks if a header key exists.
func (oh OrderedHeaders) Has(key string) bool {
	for _, h := range oh {
		if len(h) >= 1 && strings.EqualFold(h[0], key) {
			return true
		}
	}
	return false
}

// ToMap converts ordered headers to a map (loses ordering).
func (oh OrderedHeaders) ToMap() map[string]string {
	m := make(map[string]string, len(oh))
	for _, h := range oh {
		if len(h) >= 2 {
			m[h[0]] = h[1]
		}
	}
	return m
}

// Clone creates a deep copy of the ordered headers.
func (oh OrderedHeaders) Clone() OrderedHeaders {
	clone := make(OrderedHeaders, len(oh))
	for i, h := range oh {
		clone[i] = make([]string, len(h))
		copy(clone[i], h)
	}
	return clone
}

// HttpDoer is the common interface for HTTP clients used by registration flows.
type HttpDoer interface {
	Get(url string, headers map[string]string) (*Response, error)
	PostJSON(url string, headers map[string]string, body interface{}) (*Response, error)
	PostForm(url string, headers map[string]string, body string) (*Response, error)
	PostRaw(url string, headers map[string]string, body []byte) (*Response, error)
	DoWithRetry(method, url string, headers map[string]string, body []byte, retries int) (*Response, error)

	// Ordered header variants for browser fingerprint consistency
	GetOrdered(url string, headers OrderedHeaders) (*Response, error)
	PostJSONOrdered(url string, headers OrderedHeaders, body interface{}) (*Response, error)
	PostFormOrdered(url string, headers OrderedHeaders, body string) (*Response, error)
	PostRawOrdered(url string, headers OrderedHeaders, body []byte) (*Response, error)

	SetCookie(name, value string)
	GetCookie(name string) string
	Cookies() map[string]string
	CookieString(keys ...string) string
	SaveCookies(resp *Response)
	Close()
}

// HTTPClient wraps azuretls.Session with browser TLS+HTTP/2 fingerprint and proxy support.
type HTTPClient struct {
	mu       sync.RWMutex
	session  *azuretls.Session
	cookies  map[string]string
	proxyURL string
	proxyOn  bool
	browser  string // azuretls browser key: "chrome","firefox","safari","edge" (empty = chrome default)
}

// Response wraps http.Response for convenient access.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func (r *Response) Text() string {
	return string(r.Body)
}

func (r *Response) JSON(v interface{}) error {
	return json.Unmarshal(r.Body, v)
}

// Location returns the Location header value.
func (r *Response) Location() string {
	return r.Headers.Get("Location")
}

// GetAllSetCookies returns all Set-Cookie header values.
func (r *Response) GetAllSetCookies() []string {
	return r.Headers.Values("Set-Cookie")
}

// NewHTTPClient creates a new HTTP client with Chrome TLS+HTTP/2 fingerprint (default).
func NewHTTPClient(proxyURL string, proxyOn bool) *HTTPClient {
	return NewHTTPClientWithBrowser(proxyURL, proxyOn, "")
}

// NewHTTPClientWithBrowser creates an HTTP client with the specified browser TLS fingerprint.
// browser: "chrome","firefox","safari","edge" (empty = chrome default).
func NewHTTPClientWithBrowser(proxyURL string, proxyOn bool, browser string) *HTTPClient {
	c := &HTTPClient{
		cookies:  make(map[string]string),
		proxyURL: proxyURL,
		proxyOn:  proxyOn,
		browser:  browser,
	}
	c.session = c.buildSession()
	return c
}

func (c *HTTPClient) buildSession() *azuretls.Session {
	session := azuretls.NewSession()
	session.InsecureSkipVerify = true
	session.SetTimeout(60 * time.Second)

	if c.browser != "" {
		session.Browser = c.browser
	}

	if c.proxyOn && c.proxyURL != "" {
		if err := session.SetProxy(c.proxyURL); err != nil {
			Logf("[HTTP] 设置代理失败: %v", err)
		}
	}

	return session
}

// Close closes the underlying session and releases resources.
func (c *HTTPClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		c.session.Close()
	}
}

// SetProxy updates the proxy settings at runtime.
func (c *HTTPClient) SetProxy(proxyURL string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.proxyURL = proxyURL
	c.proxyOn = on
	// Close old session and rebuild to clear connection pool
	if c.session != nil {
		c.session.Close()
	}
	c.session = c.buildSession()
}

// SetCookie sets a cookie.
func (c *HTTPClient) SetCookie(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cookies[name] = value
}

// GetCookie gets a cookie value.
func (c *HTTPClient) GetCookie(name string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cookies[name]
}

// Cookies returns a copy of all cookies.
func (c *HTTPClient) Cookies() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make(map[string]string, len(c.cookies))
	for k, v := range c.cookies {
		cp[k] = v
	}
	return cp
}

// CookieString returns cookies formatted for the Cookie header.
// If no keys are specified, all cookies are returned sorted alphabetically by key.
// If keys are specified, cookies are returned in the specified order.
func (c *HTTPClient) CookieString(keys ...string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var parts []string
	if len(keys) == 0 {
		sortedKeys := make([]string, 0, len(c.cookies))
		for k := range c.cookies {
			sortedKeys = append(sortedKeys, k)
		}
		sort.Strings(sortedKeys)
		for _, k := range sortedKeys {
			parts = append(parts, k+"="+c.cookies[k])
		}
	} else {
		for _, k := range keys {
			if v, ok := c.cookies[k]; ok {
				parts = append(parts, k+"="+v)
			}
		}
	}
	return strings.Join(parts, "; ")
}

// SaveCookies parses Set-Cookie headers from a response.
func (c *HTTPClient) SaveCookies(resp *Response) {
	skip := map[string]bool{
		"path": true, "domain": true, "expires": true,
		"max-age": true, "secure": true, "httponly": true, "samesite": true,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range resp.GetAllSetCookies() {
		if !strings.Contains(sc, "=") {
			continue
		}
		kv := strings.SplitN(sc, ";", 2)[0]
		kv = strings.TrimSpace(kv)
		eqIdx := strings.Index(kv, "=")
		if eqIdx <= 0 {
			continue
		}
		name := strings.TrimSpace(kv[:eqIdx])
		value := strings.TrimSpace(kv[eqIdx+1:])
		if !skip[strings.ToLower(name)] && name != "" {
			c.cookies[name] = value
		}
	}
}

// Get performs a GET request.
func (c *HTTPClient) Get(urlStr string, headers map[string]string) (*Response, error) {
	return c.do("GET", urlStr, headers, nil)
}

// PostJSON performs a POST with JSON body.
func (c *HTTPClient) PostJSON(urlStr string, headers map[string]string, body interface{}) (*Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	if _, ok := headers["Content-Type"]; !ok {
		headers["Content-Type"] = "application/json"
	}
	return c.do("POST", urlStr, headers, data)
}

// PostForm performs a POST with form-encoded body.
func (c *HTTPClient) PostForm(urlStr string, headers map[string]string, body string) (*Response, error) {
	if headers == nil {
		headers = make(map[string]string)
	}
	if _, ok := headers["Content-Type"]; !ok {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	return c.do("POST", urlStr, headers, []byte(body))
}

// PostRaw performs a POST with raw bytes body.
func (c *HTTPClient) PostRaw(urlStr string, headers map[string]string, body []byte) (*Response, error) {
	return c.do("POST", urlStr, headers, body)
}

// GetOrdered performs a GET request with ordered headers for browser fingerprint consistency.
func (c *HTTPClient) GetOrdered(urlStr string, headers OrderedHeaders) (*Response, error) {
	return c.doOrdered("GET", urlStr, headers, nil)
}

// PostJSONOrdered performs a POST with JSON body using ordered headers.
func (c *HTTPClient) PostJSONOrdered(urlStr string, headers OrderedHeaders, body interface{}) (*Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if !headers.Has("Content-Type") {
		headers.Set("Content-Type", "application/json")
	}
	return c.doOrdered("POST", urlStr, headers, data)
}

// PostFormOrdered performs a POST with form-encoded body using ordered headers.
func (c *HTTPClient) PostFormOrdered(urlStr string, headers OrderedHeaders, body string) (*Response, error) {
	if !headers.Has("Content-Type") {
		headers.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return c.doOrdered("POST", urlStr, headers, []byte(body))
}

// PostRawOrdered performs a POST with raw bytes body using ordered headers.
func (c *HTTPClient) PostRawOrdered(urlStr string, headers OrderedHeaders, body []byte) (*Response, error) {
	return c.doOrdered("POST", urlStr, headers, body)
}

func (c *HTTPClient) do(method, urlStr string, headers map[string]string, body []byte) (*Response, error) {
	req := &azuretls.Request{
		Method:           method,
		Url:              urlStr,
		DisableRedirects: true,
	}

	if headers != nil {
		h := make(fhttp.Header)
		for k, v := range headers {
			h.Set(k, v)
		}
		req.Header = h
	}

	if body != nil {
		req.Body = body
	}

	c.mu.RLock()
	session := c.session
	proxyOn := c.proxyOn
	proxyURL := c.proxyURL
	c.mu.RUnlock()
	resp, err := session.Do(req)
	if err != nil {
		if proxyOn && proxyURL != "" {
			AddProxyTraffic(int64(len(body))+256, 0)
		}
		return nil, err
	}

	respHeaders := make(http.Header)
	if resp.HttpResponse != nil {
		for k, v := range resp.HttpResponse.Header {
			respHeaders[k] = v
		}
	}

	if proxyOn && proxyURL != "" {
		AddProxyTraffic(int64(len(body))+256, int64(len(resp.Body)))
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Headers:    respHeaders,
		Body:       resp.Body,
	}, nil
}

// doOrdered performs a request with ordered headers using azuretls.OrderedHeaders.
func (c *HTTPClient) doOrdered(method, urlStr string, headers OrderedHeaders, body []byte) (*Response, error) {
	req := &azuretls.Request{
		Method:           method,
		Url:              urlStr,
		DisableRedirects: true,
	}

	if len(headers) > 0 {
		oh := make(azuretls.OrderedHeaders, 0, len(headers))
		for _, h := range headers {
			if len(h) >= 2 {
				oh = append(oh, h)
			}
		}
		req.OrderedHeaders = oh
	}

	if body != nil {
		req.Body = body
	}

	c.mu.RLock()
	session := c.session
	proxyOn := c.proxyOn
	proxyURL := c.proxyURL
	c.mu.RUnlock()
	resp, err := session.Do(req)
	if err != nil {
		if proxyOn && proxyURL != "" {
			AddProxyTraffic(int64(len(body))+256, 0)
		}
		return nil, err
	}

	respHeaders := make(http.Header)
	if resp.HttpResponse != nil {
		for k, v := range resp.HttpResponse.Header {
			respHeaders[k] = v
		}
	}

	if proxyOn && proxyURL != "" {
		AddProxyTraffic(int64(len(body))+256, int64(len(resp.Body)))
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Headers:    respHeaders,
		Body:       resp.Body,
	}, nil
}

// DoWithRetry performs a request with retry on connection errors.
func (c *HTTPClient) DoWithRetry(method, urlStr string, headers map[string]string, body []byte, retries int) (*Response, error) {
	var lastErr error
	for i := 0; i < retries; i++ {
		resp, err := c.do(method, urlStr, headers, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		Logf("[HTTP] %s %s 失败 (第%d次): %v", method, safeGrokURLForLog(urlStr), i+1, err)
		if i < retries-1 {
			time.Sleep(time.Duration(2*(i+1)) * time.Second)
		}
	}
	return nil, fmt.Errorf("重试 %d 次仍失败: %w", retries, lastErr)
}
