package grokregister

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// generateHumanLikeUsername creates realistic usernames with name+birthday style formats.
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

	pattern := rand.Intn(100)
	first := firstNamesPool[rand.Intn(len(firstNamesPool))]
	last := lastNamesPool[rand.Intn(len(lastNamesPool))]
	year := 1975 + rand.Intn(31) // 1975-2005
	month := 1 + rand.Intn(12)
	day := 1 + rand.Intn(28)
	dobFull := fmt.Sprintf("%04d%02d%02d", year, month, day)
	dobShort := fmt.Sprintf("%02d%02d%02d", year%100, month, day)

	switch {
	case pattern < 30:
		return first + last + dobFull // jamessmith19980417
	case pattern < 50:
		return first + last + dobShort // jamessmith980417
	case pattern < 65:
		return first + dobFull // james19980417
	case pattern < 80:
		return last + first + dobShort // smithjames980417
	case pattern < 90:
		return first + last + fmt.Sprintf("%02d", rand.Intn(100)) // jamessmith47
	default:
		return first + last[:min(4, len(last))] + fmt.Sprintf("%04d", rand.Intn(10000)) // jamessmit2741
	}
}

// makeProxyHTTPClient creates an http.Client that routes through the given proxy (socks5/http/https).
// When proxyURL is set, responses are counted into proxy traffic stats.
func makeProxyHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return &http.Client{Timeout: timeout}
	}
	transport := &http.Transport{}
	if u.Scheme == "socks5" || u.Scheme == "socks5h" {
		dialer, derr := proxy.FromURL(u, proxy.Direct)
		if derr == nil {
			if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
				transport.DialContext = ctxDialer.DialContext
			} else {
				transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			}
		}
	} else {
		transport.Proxy = http.ProxyURL(u)
	}
	return &http.Client{
		Transport: countingRoundTripper{base: transport},
		Timeout:   timeout,
	}
}

// dialTLSThroughProxy dials a TLS connection through a SOCKS5/HTTP proxy. Falls back to direct if proxyURL is empty.
func dialTLSThroughProxy(proxyURL, addr string) (net.Conn, error) {
	if proxyURL == "" {
		return tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	}
	dialer, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	}
	rawConn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("代理连接失败: %w", err)
	}
	host, _, _ := net.SplitHostPort(addr)
	tlsConn := tls.Client(rawConn, &tls.Config{ServerName: host, InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("TLS 握手失败: %w", err)
	}
	return tlsConn, nil
}

// OTPEntry holds a pushed OTP code and optional raw email body for custom extraction.
type OTPEntry struct {
	Code string
	Body string
}

// EmailService interface for getting verification codes.
type EmailService interface {
	Create() (string, error)
	WaitForCode(timeout time.Duration, interval time.Duration) (string, error)
	Address() string
	ResetForRetry() error // refresh token + preCount for retry
}

// ── Temporam temporary email ──

const (
	temporamWebBase   = "https://www.temporam.com"
	temporamAPIV1Base = "https://api.temporam.com/v1" // 官方商用 API（需 Bearer），见文档
)

var fallbackDomains = []string{"nooboy.com", "tianmi.me", "temporam.xin", "guanshuyun.com"}

type TempEmail struct {
	address       string
	session       *http.Client
	proxy         string
	domains       []string
	cancel        *SafeCancel
	logPrefix     string
	codeExtractor func(string) string // optional override (e.g. Grok XXX-XXX)
	apiKey        string              // 非空则走 api.temporam.com/v1 + Bearer
}

func (t *TempEmail) SetCancel(ch *SafeCancel)                { t.cancel = ch }
func (t *TempEmail) SetLogPrefix(prefix string)              { t.logPrefix = prefix }
func (t *TempEmail) SetCodeExtractor(fn func(string) string) { t.codeExtractor = fn }

// SetAPIKey 设置后拉域名与收信均走 https://api.temporam.com/v1（Authorization: Bearer …）
func (t *TempEmail) SetAPIKey(key string) { t.apiKey = strings.TrimSpace(key) }

func (t *TempEmail) temporamUseV1() bool { return t.apiKey != "" }

// TemporamChannel 返回收信所用通道，便于 OpenAI 等任务日志排查（公开 API vs 官方 v1）。
func (t *TempEmail) TemporamChannel() string {
	if t.temporamUseV1() {
		return "api.temporam.com/v1"
	}
	return "www.temporam.com 公开 API"
}

func (t *TempEmail) temporamSetReqHeaders(req *http.Request, webReferer bool) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	if t.temporamUseV1() {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	} else if webReferer {
		req.Header.Set("Referer", temporamWebBase+"/zh")
	}
}

func NewTempEmail(proxyURL string) *TempEmail {
	return &TempEmail{
		session: makeProxyHTTPClient(proxyURL, 15*time.Second),
		proxy:   proxyURL,
	}
}

func (t *TempEmail) Address() string { return t.address }

func (t *TempEmail) loadDomains() []string {
	var reqURL string
	if t.temporamUseV1() {
		reqURL = temporamAPIV1Base + "/domains"
	} else {
		// 官网公开 /api/domains（无 Key）
		reqURL = temporamWebBase + "/api/domains"
	}
	req, _ := http.NewRequest("GET", reqURL, nil)
	t.temporamSetReqHeaders(req, !t.temporamUseV1())

	resp, err := t.session.Do(req)
	if err != nil {
		return fallbackDomains
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if t.temporamUseV1() {
		var wrapper struct {
			Code int `json:"code"`
			Data []struct {
				Domain string `json:"domain"`
				Type   int    `json:"type"` // 0 与官网免费域一致；非 0 视为需订阅类
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &wrapper); err != nil || wrapper.Code != 200 || len(wrapper.Data) == 0 {
			Logf("%s[Temporam] v1 拉取域名失败或为空，使用内置回退列表", t.logPrefix)
			return fallbackDomains
		}
		var filtered []string
		for _, d := range wrapper.Data {
			if d.Domain == "" || d.Type != 0 {
				continue
			}
			filtered = append(filtered, d.Domain)
		}
		if len(filtered) == 0 {
			for _, d := range wrapper.Data {
				if d.Domain != "" {
					filtered = append(filtered, d.Domain)
				}
			}
			if len(filtered) > 0 {
				Logf("%s[Temporam] v1 域名列表无 type=0 项，已使用全部返回域名", t.logPrefix)
			}
		}
		if len(filtered) == 0 {
			return fallbackDomains
		}
		return filtered
	}

	var wrapper struct {
		Code int `json:"code"`
		Data []struct {
			Domain         string `json:"domain"`
			IsSubscription bool   `json:"isSubscription"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil || wrapper.Code != 200 || len(wrapper.Data) == 0 {
		return fallbackDomains
	}
	var filtered []string
	for _, d := range wrapper.Data {
		if d.Domain == "" || d.IsSubscription {
			continue
		}
		filtered = append(filtered, d.Domain)
	}
	if len(filtered) == 0 {
		return fallbackDomains
	}
	return filtered
}

func (t *TempEmail) Create() (string, error) {
	if len(t.domains) == 0 {
		t.domains = t.loadDomains()
	}
	user := generateHumanLikeUsername()
	domain := t.domains[rand.Intn(len(t.domains))]
	t.address = user + "@" + domain
	Logf("%s临时邮箱: %s", t.logPrefix, t.address)
	return t.address, nil
}

func (t *TempEmail) temporamEmailSinceParam() string {
	// since 是「邮件时间下限」：只返回晚于该时刻的邮件（与官网一致，约最近 10 分钟内的信）。
	// 这不是轮询间隔——轮询多快由 WaitForCode 的 interval / burst（如 500ms）决定。
	return time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

// temporamListMailMeta 官网：GET /api/emails；有 API Key：GET https://api.temporam.com/v1/emails
func (t *TempEmail) temporamListMailMeta() ([]struct {
	ID        string `json:"id"`
	Subject   string `json:"subject"`
	FromEmail string `json:"fromEmail"`
}, error) {
	q := url.Values{}
	q.Set("email", t.address)
	q.Set("since", t.temporamEmailSinceParam())
	q.Set("limit", "50")
	listURL := temporamWebBase + "/api/emails?" + q.Encode()
	if t.temporamUseV1() {
		listURL = temporamAPIV1Base + "/emails?" + q.Encode()
	}
	req, _ := http.NewRequest("GET", listURL, nil)
	t.temporamSetReqHeaders(req, !t.temporamUseV1())

	resp, err := t.session.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var wrapper struct {
		Code int `json:"code"`
		Data []struct {
			ID        string `json:"id"`
			Subject   string `json:"subject"`
			FromEmail string `json:"fromEmail"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &wrapper) != nil || wrapper.Code != 200 {
		return nil, fmt.Errorf("Temporam 邮件列表解析失败")
	}
	return wrapper.Data, nil
}

func (t *TempEmail) temporamFetchMailContent(id string) (subject, html string, err error) {
	detailURL := temporamWebBase + "/api/emails/" + url.PathEscape(id)
	if t.temporamUseV1() {
		detailURL = temporamAPIV1Base + "/emails/" + url.PathEscape(id)
	}
	req, _ := http.NewRequest("GET", detailURL, nil)
	t.temporamSetReqHeaders(req, !t.temporamUseV1())
	resp, err := t.session.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var wrapper struct {
		Code int `json:"code"`
		Data *struct {
			Subject string `json:"subject"`
			Content string `json:"content"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &wrapper) != nil || wrapper.Code != 200 || wrapper.Data == nil {
		return "", "", fmt.Errorf("Temporam 邮件详情解析失败")
	}
	return wrapper.Data.Subject, wrapper.Data.Content, nil
}

func (t *TempEmail) WaitForCode(timeout, interval time.Duration) (string, error) {
	if t.address == "" {
		return "", fmt.Errorf("邮箱未创建")
	}
	extract := extractCode
	if t.codeExtractor != nil {
		extract = t.codeExtractor
	}
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	// 与首页「手动刷新」等价：每次循环都是一次 GET /api/emails；前若干轮缩短间隔 ≈ 连续点击刷新，尽快拿到验证码。
	const (
		temporamBurstPolls = 24
		temporamBurstGap   = 500 * time.Millisecond
	)
	for i := 1; ; i++ {
		if isCancelled(t.cancel) {
			return "", fmt.Errorf("cancelled")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待验证码超时 (%v)", timeout)
		}
		items, err := t.temporamListMailMeta()
		if err == nil && len(items) > 0 {
			for _, m := range items {
				if code := extract(m.Subject + " " + m.FromEmail); code != "" {
					Logf("%s验证码: %s", t.logPrefix, code)
					return code, nil
				}
				if strings.TrimSpace(m.ID) == "" {
					continue
				}
				subj, content, derr := t.temporamFetchMailContent(m.ID)
				if derr != nil {
					continue
				}
				plain := stripHTML(content)
				if code := extract(subj + " " + plain); code != "" {
					Logf("%s验证码: %s", t.logPrefix, code)
					return code, nil
				}
			}
		}
		if i%6 == 0 {
			Logf("%s[Temporam] 已刷新 %d 次，继续等待邮件...", t.logPrefix, i)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待验证码超时 (%v)", timeout)
		}
		nextSleep := interval
		if i < temporamBurstPolls && interval > temporamBurstGap {
			nextSleep = temporamBurstGap
		}
		if rem := time.Until(deadline); nextSleep > rem {
			nextSleep = rem
		}
		if nextSleep <= 0 {
			return "", fmt.Errorf("等待验证码超时 (%v)", timeout)
		}
		if sleepWithCancel(nextSleep, t.cancel) {
			return "", fmt.Errorf("cancelled")
		}
	}
}

func (t *TempEmail) ResetForRetry() error { return nil }

// FetchTemporamDomains 无 apiKey 走官网公开接口；有 apiKey 走 https://api.temporam.com/v1/domains
func FetchTemporamDomains(proxyURL, apiKey string) []string {
	t := NewTempEmail(proxyURL)
	if strings.TrimSpace(apiKey) != "" {
		t.SetAPIKey(apiKey)
	}
	return t.loadDomains()
}

// ── Code extraction ──

var codePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:verification code|security code|one(?:-| )?time password|otp|验证码|验证代码|校验码|amazon q|builder id|kiro)[^\d]{0,32}(\d{6})`),
	regexp.MustCompile(`(\d{6})\s*(?:is your|为您的|是您的|是你的)\s*(?:verification code|security code|one(?:-| )?time password|otp|验证码|验证代码|校验码)?`),
}

var skipCodes = map[string]bool{"000000": true, "111111": true, "123456": true, "555555": true, "222222": true, "333333": true, "444444": true, "666666": true, "777777": true, "888888": true, "999999": true}
var genericCodePattern = regexp.MustCompile(`\b(\d{6})\b`)
var verificationContextTerms = []string{"verification code", "security code", "one-time password", "one time password", "otp", "验证码", "验证代码", "校验码", "amazon q", "builder id", "kiro"}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var htmlSpaceRe = regexp.MustCompile(`\s+`)

// stripHTML removes all HTML tags and collapses whitespace, leaving only visible text.
func stripHTML(s string) string {
	// Remove style and script blocks entirely
	s = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(s, " ")
	// Remove HTML tags
	s = htmlTagRe.ReplaceAllString(s, " ")
	// Decode common entities
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	// Collapse whitespace
	s = htmlSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func hasVerificationContext(text string) bool {
	lower := strings.ToLower(text)
	for _, term := range verificationContextTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func extractCode(text string) string {
	for _, p := range codePatterns {
		m := p.FindStringSubmatch(text)
		if len(m) > 1 && !skipCodes[m[1]] {
			return m[1]
		}
	}
	if !hasVerificationContext(text) {
		return ""
	}
	matches := genericCodePattern.FindAllStringSubmatch(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		if len(matches[i]) > 1 && !skipCodes[matches[i][1]] {
			return matches[i][1]
		}
	}
	return ""
}

// ── Outlook email via IMAP (matching reg.yinxh.fun approach) ──

const (
	msTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	imapServer = "outlook.live.com:993"
)

type OutlookEmail struct {
	address         string // mailbox address for OAuth/IMAP
	registerAddress string // optional +tag address used for registration
	plusTag         string
	password        string
	clientID        string
	refreshToken    string
	accessToken     string
	session         *http.Client
	proxyURL        string
	preCount        int // 发送前邮件数
	cancel          *SafeCancel
	logPrefix       string
	codeExtractor   func(string) string
	keepRawHTML     bool // true 时邮件正文不剥离 HTML，便于从 <a href> 提取确认链接（ecomagent 用）
}

func (o *OutlookEmail) SetCancel(ch *SafeCancel)                { o.cancel = ch }
func (o *OutlookEmail) SetLogPrefix(prefix string)              { o.logPrefix = prefix }
func (o *OutlookEmail) SetCodeExtractor(fn func(string) string) { o.codeExtractor = fn }
func (o *OutlookEmail) SetKeepRawHTML(v bool)                   { o.keepRawHTML = v }

// cleanBody 默认剥离 HTML 取可见文本；keepRawHTML=true 时保留原文（供确认链接提取）。
func (o *OutlookEmail) cleanBody(s string) string {
	if o.keepRawHTML {
		return s
	}
	return stripHTML(s)
}

func NewOutlookEmail(email, password, clientID, refreshToken, proxyURL string) *OutlookEmail {
	return &OutlookEmail{
		address:      email,
		password:     password,
		clientID:     clientID,
		refreshToken: refreshToken,
		session:      makeProxyHTTPClient(proxyURL, 30*time.Second),
		proxyURL:     proxyURL,
		preCount:     -1,
	}
}

func (o *OutlookEmail) MailboxAddress() string { return o.address }

func (o *OutlookEmail) PlusTag() string { return o.plusTag }

func (o *OutlookEmail) SetRegisterAddress(registerEmail, tag string) {
	o.registerAddress = strings.TrimSpace(registerEmail)
	o.plusTag = strings.TrimSpace(tag)
}

func (o *OutlookEmail) Address() string {
	if strings.TrimSpace(o.registerAddress) != "" {
		return o.registerAddress
	}
	return o.address
}

func (o *OutlookEmail) Create() (string, error) {
	reg := o.Address()
	Logf("%sOutlook 邮箱: %s (mailbox=%s)", o.logPrefix, reg, o.address)
	// Refresh token and get pre-send email count
	if err := o.refreshAccessToken(); err != nil {
		return "", err
	}
	count, err := o.imapGetCount()
	if err != nil {
		Logf("%s[IMAP] 获取发送前邮件数失败: %v (继续)", o.logPrefix, err)
		if o.preCount < 0 {
			o.preCount = 0
		}
	} else {
		o.preCount = count
		Logf("%s发送前邮件数: %d", o.logPrefix, count)
	}
	return reg, nil
}

func (o *OutlookEmail) refreshAccessToken() error {
	data := fmt.Sprintf("client_id=%s&grant_type=refresh_token&refresh_token=%s",
		url.QueryEscape(o.clientID), url.QueryEscape(o.refreshToken))
	resp, err := o.session.Post(msTokenURL, "application/x-www-form-urlencoded", strings.NewReader(data))
	if err != nil {
		return fmt.Errorf("token 刷新失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("token 刷新失败: %d %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(body, &result)
	o.accessToken = result.AccessToken
	if result.RefreshToken != "" {
		o.refreshToken = result.RefreshToken
	}
	return nil
}

// ValidateToken 仅校验 refreshToken 是否有效（账号体检用，不连接 IMAP）。
func (o *OutlookEmail) ValidateToken() error {
	return o.refreshAccessToken()
}

// imapConnect connects to IMAP server with XOAUTH2 authentication
func (o *OutlookEmail) imapConnect() (net.Conn, *bufio.Reader, error) {
	conn, err := dialTLSThroughProxy(o.proxyURL, imapServer)
	if err != nil {
		return nil, nil, fmt.Errorf("IMAP 连接失败: %w", err)
	}
	reader := bufio.NewReader(conn)

	// Read greeting
	greeting, _ := reader.ReadString('\n')
	Logf("%s[IMAP] %s", o.logPrefix, strings.TrimSpace(greeting))

	// XOAUTH2 authentication
	authStr := fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", o.address, o.accessToken)
	encoded := base64.StdEncoding.EncodeToString([]byte(authStr))
	fmt.Fprintf(conn, "A1 AUTHENTICATE XOAUTH2 %s\r\n", encoded)

	authResp, _ := reader.ReadString('\n')
	if !strings.Contains(authResp, "OK") {
		conn.Close()
		return nil, nil, fmt.Errorf("IMAP 认证失败: %s", strings.TrimSpace(authResp))
	}
	Logf("%s[IMAP] 认证成功", o.logPrefix)
	return conn, reader, nil
}

// imapGetCount returns number of messages in INBOX
func (o *OutlookEmail) imapGetCount() (int, error) {
	conn, reader, err := o.imapConnect()
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	fmt.Fprintf(conn, "A2 SELECT INBOX\r\n")
	count := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.Contains(line, "EXISTS") {
			fmt.Sscanf(line, "* %d EXISTS", &count)
		}
		if strings.HasPrefix(line, "A2 ") {
			break
		}
	}

	fmt.Fprintf(conn, "A3 LOGOUT\r\n")
	return count, nil
}

// imapFetchLatest fetches the latest emails after preCount
func (o *OutlookEmail) imapFetchLatest() ([]string, error) {
	conn, reader, err := o.imapConnect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Select INBOX
	fmt.Fprintf(conn, "A2 SELECT INBOX\r\n")
	totalCount := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.Contains(line, "EXISTS") {
			fmt.Sscanf(line, "* %d EXISTS", &totalCount)
		}
		if strings.HasPrefix(line, "A2 ") {
			break
		}
	}

	if totalCount <= o.preCount {
		fmt.Fprintf(conn, "A9 LOGOUT\r\n")
		return nil, nil
	}

	// Fetch body text of new messages
	start := o.preCount + 1
	if start < 1 {
		start = 1
	}
	fetchRange := fmt.Sprintf("%d:%d", start, totalCount)
	fmt.Fprintf(conn, "A3 FETCH %s BODY.PEEK[TEXT]\r\n", fetchRange)

	var texts []string
	var buf strings.Builder
	capturing := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "A3 ") {
			if buf.Len() > 0 {
				texts = append(texts, o.cleanBody(buf.String()))
				buf.Reset()
			}
			break
		}
		if strings.HasPrefix(trimmed, "*") && strings.Contains(trimmed, "FETCH") {
			if buf.Len() > 0 {
				texts = append(texts, o.cleanBody(buf.String()))
				buf.Reset()
			}
			capturing = true
			continue
		}
		if capturing {
			if trimmed == ")" {
				continue
			}
			buf.WriteString(line)
		}
	}

	fmt.Fprintf(conn, "A9 LOGOUT\r\n")
	return texts, nil
}

// imapSelectAndFetch selects a folder, checks for new messages beyond preCount, fetches and extracts codes.
// Returns (code, currentTotalCount, newTagNum, error). code="" means no code found yet.
func (o *OutlookEmail) imapSelectAndFetch(conn net.Conn, reader *bufio.Reader, folder string, preCount int, tagNum int) (string, int, int, error) {
	tag := fmt.Sprintf("B%d", tagNum)
	tagNum++
	fmt.Fprintf(conn, "%s SELECT \"%s\"\r\n", tag, folder)
	totalCount := 0
	for {
		line, lerr := reader.ReadString('\n')
		if lerr != nil {
			return "", preCount, tagNum, lerr
		}
		line = strings.TrimSpace(line)
		if strings.Contains(line, "EXISTS") {
			fmt.Sscanf(line, "* %d EXISTS", &totalCount)
		}
		if strings.HasPrefix(line, tag+" ") {
			break
		}
	}

	if totalCount <= preCount {
		return "", totalCount, tagNum, nil
	}

	start := preCount + 1
	if start < 1 {
		start = 1
	}
	fetchTag := fmt.Sprintf("B%d", tagNum)
	tagNum++
	fetchRange := fmt.Sprintf("%d:%d", start, totalCount)
	fmt.Fprintf(conn, "%s FETCH %s BODY.PEEK[TEXT]\r\n", fetchTag, fetchRange)

	var texts []string
	var buf strings.Builder
	capturing := false
	var readErr error
	for {
		line, lerr := reader.ReadString('\n')
		if lerr != nil {
			readErr = lerr
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, fetchTag+" ") {
			if buf.Len() > 0 {
				texts = append(texts, o.cleanBody(buf.String()))
			}
			break
		}
		if strings.HasPrefix(trimmed, "*") && strings.Contains(trimmed, "FETCH") {
			if buf.Len() > 0 {
				texts = append(texts, o.cleanBody(buf.String()))
				buf.Reset()
			}
			capturing = true
			continue
		}
		if capturing {
			if trimmed == ")" {
				continue
			}
			buf.WriteString(line)
		}
	}

	extractor := extractCode
	if o.codeExtractor != nil {
		extractor = o.codeExtractor
	}
	for i := len(texts) - 1; i >= 0; i-- {
		text := texts[i]
		if code := extractor(text); code != "" {
			return code, totalCount, tagNum, nil
		}
	}
	if readErr != nil {
		return "", totalCount, tagNum, fmt.Errorf("IMAP FETCH 读取中断: %w", readErr)
	}
	return "", totalCount, tagNum, nil
}

func (o *OutlookEmail) WaitForCode(timeout, interval time.Duration) (string, error) {
	Logf("%s[Outlook IMAP] 等待验证码, 邮箱=%s, 发送前邮件数=%d", o.logPrefix, o.address, o.preCount)

	conn, reader, err := o.imapConnect()
	reconnect := func(reason string) error {
		Logf("%s[Outlook IMAP] %s，刷新 token 后重连...", o.logPrefix, reason)
		if rerr := o.refreshAccessToken(); rerr != nil {
			Logf("%s[Outlook IMAP] 刷新 token 失败(继续重连): %v", o.logPrefix, rerr)
		}
		if conn != nil {
			conn.Close()
		}
		conn, reader, err = o.imapConnect()
		if err != nil {
			return fmt.Errorf("IMAP 重连失败: %w", err)
		}
		return nil
	}
	if err != nil {
		if rerr := o.refreshAccessToken(); rerr != nil {
			return "", rerr
		}
		conn, reader, err = o.imapConnect()
		if err != nil {
			return "", fmt.Errorf("IMAP 连接失败: %w", err)
		}
	}
	defer conn.Close()

	junkPreCount := -1 // -1 表示还未初始化，第一次 SELECT Junk 时记录
	maxRetries := int(timeout / interval)
	tagNum := 2
	for i := 1; i <= maxRetries; i++ {
		if isCancelled(o.cancel) {
			fmt.Fprintf(conn, "Z1 LOGOUT\r\n")
			return "", fmt.Errorf("cancelled")
		}

		// 检查 INBOX
		code, _, newTag, lerr := o.imapSelectAndFetch(conn, reader, "INBOX", o.preCount, tagNum)
		tagNum = newTag
		if lerr != nil {
			if err := reconnect("INBOX 读取失败"); err != nil {
				return "", err
			}
			continue
		}
		if code != "" {
			Logf("%s[Outlook IMAP] 获取到验证码(INBOX): %s", o.logPrefix, code)
			fmt.Fprintf(conn, "Z1 LOGOUT\r\n")
			return code, nil
		}

		// 每 3 次轮询也检查 Junk 文件夹 (验证邮件可能被误判为垃圾邮件)
		if i%3 == 0 || junkPreCount == -1 {
			code, junkCnt, newTag, jerr := o.imapSelectAndFetch(conn, reader, "Junk", junkPreCount, tagNum)
			tagNum = newTag
			if jerr != nil {
				if err := reconnect("Junk 读取失败"); err != nil {
					return "", err
				}
				continue
			}
			if junkPreCount == -1 {
				junkPreCount = junkCnt
				Logf("%s[Outlook IMAP] Junk 文件夹初始邮件数: %d", o.logPrefix, junkPreCount)
			}
			if code != "" {
				Logf("%s[Outlook IMAP] 获取到验证码(Junk): %s", o.logPrefix, code)
				fmt.Fprintf(conn, "Z1 LOGOUT\r\n")
				return code, nil
			}
		}

		if i%6 == 0 {
			Logf("%s[Outlook IMAP] [%d/%d] 等待验证码邮件... 邮箱=%s (INBOX+Junk)", o.logPrefix, i, maxRetries, o.address)
		}
		if sleepWithCancel(interval, o.cancel) {
			fmt.Fprintf(conn, "Z1 LOGOUT\r\n")
			return "", fmt.Errorf("cancelled")
		}
	}
	fmt.Fprintf(conn, "Z1 LOGOUT\r\n")
	return "", fmt.Errorf("Outlook IMAP 等待验证码超时 (%v)", timeout)
}

func (o *OutlookEmail) ResetForRetry() error {
	Logf("%s[Outlook] 重试前刷新 token, 邮箱=%s", o.logPrefix, o.address)
	if err := o.refreshAccessToken(); err != nil {
		return fmt.Errorf("刷新token失败: %w", err)
	}
	count, err := o.imapGetCount()
	if err != nil {
		Logf("%s[IMAP] 重试前获取邮件数失败: %v (使用旧值)", o.logPrefix, err)
	} else {
		o.preCount = count
		Logf("%s[Outlook] 重试前邮件数: %d", o.logPrefix, count)
	}
	return nil
}

// ── Cloudflare Email Worker ──

// Browser-like UA so Cloudflare edge bot rules (error 1010) do not block Worker API polls.
const cfWorkerHTTPUserAgent = "Mozilla/5.0 (compatible; sub2api-cfworker/1; +https://kiro.p-box.online)"

type CFWorkerEmail struct {
	address       string
	domain        string
	workerURL     string // e.g. https://kiro-email-worker.xxx.workers.dev or custom domain
	apiKey        string
	cfAccountID   string // Cloudflare Account ID (for KV REST API)
	cfApiToken    string // Cloudflare API Token (for KV REST API)
	kvNamespaceID string // KV Namespace ID (for KV REST API)
	// useEduSubdomain: address = {random}@edu.{domain} (local-part still generateHumanLikeUsername)
	useEduSubdomain bool
	session         *http.Client
	cancel          *SafeCancel
	logPrefix       string
	codeExtractor   func(string) string
}

func (c *CFWorkerEmail) SetCancel(ch *SafeCancel)                { c.cancel = ch }
func (c *CFWorkerEmail) SetLogPrefix(prefix string)              { c.logPrefix = prefix }
func (c *CFWorkerEmail) SetCodeExtractor(fn func(string) string) { c.codeExtractor = fn }

// SetUseEduSubdomain enables xxx@edu.<root-domain> address format.
func (c *CFWorkerEmail) SetUseEduSubdomain(v bool) *CFWorkerEmail {
	if c != nil {
		c.useEduSubdomain = v
	}
	return c
}

// mailDomain is the host part after @ (root domain or edu.<root>).
func (c *CFWorkerEmail) mailDomain() string {
	d := strings.TrimSpace(c.domain)
	if d == "" {
		return d
	}
	if !c.useEduSubdomain {
		return d
	}
	// Domain already stored as edu.example.com → do not double-prefix.
	if strings.HasPrefix(strings.ToLower(d), "edu.") {
		return d
	}
	return "edu." + d
}

// Clone creates a new independent CFWorkerEmail with the same config but fresh state.
func (c *CFWorkerEmail) Clone() *CFWorkerEmail {
	return NewCFWorkerEmail(c.domain, c.workerURL, c.apiKey, c.cfAccountID, c.cfApiToken, c.kvNamespaceID).
		SetUseEduSubdomain(c.useEduSubdomain)
}

func NewCFWorkerEmail(domain, workerURL, apiKey, cfAccountID, cfApiToken, kvNamespaceID string) *CFWorkerEmail {
	return &CFWorkerEmail{
		domain:        domain,
		workerURL:     strings.TrimRight(workerURL, "/"),
		apiKey:        apiKey,
		cfAccountID:   cfAccountID,
		cfApiToken:    cfApiToken,
		kvNamespaceID: kvNamespaceID,
		session:       &http.Client{Timeout: 10 * time.Second},
	}
}

// useKVRestAPI returns true if CF API credentials are configured for direct KV reads
func (c *CFWorkerEmail) useKVRestAPI() bool {
	return c.cfAccountID != "" && c.cfApiToken != "" && c.kvNamespaceID != ""
}

func (c *CFWorkerEmail) Address() string { return c.address }

func (c *CFWorkerEmail) Create() (string, error) {
	user := generateHumanLikeUsername()
	c.address = user + "@" + c.mailDomain()
	Logf("%sCF Worker 邮箱: %s", c.logPrefix, c.address)
	// Addresses are freshly randomized. A synchronous remote DELETE here added
	// one network round trip (up to 10s) to every registration for no useful work.
	registerOTPPending(c.address)
	return c.address, nil
}

// kvResult holds the parsed KV value (code + optional raw email body).
type kvResult struct {
	Code string
	Body string
}

// readKVDirect reads OTP from Cloudflare KV REST API.
// 404/key-not-found is a normal miss while waiting for Email Routing → Worker put.
// Do NOT log 404 (it floods UI); only log auth/network/parse issues.
// Tries address dual-forms (root <-> edu.root) so format switch never orphans KV keys.
func (c *CFWorkerEmail) readKVDirect() (kvResult, error) {
	var lastErr error
	var sawNon404 bool
	for _, key := range otpEmailAliasKeys(c.address) {
		kvURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/%s",
			c.cfAccountID, c.kvNamespaceID, url.PathEscape(key))
		req, _ := http.NewRequest("GET", kvURL, nil)
		req.Header.Set("Authorization", "Bearer "+c.cfApiToken)
		req.Header.Set("User-Agent", cfWorkerHTTPUserAgent)
		resp, err := c.session.Do(req)
		if err != nil {
			// network blip — silent miss, next poll will retry
			return kvResult{}, err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == 404 {
			lastErr = fmt.Errorf("kv miss")
			continue
		}
		if resp.StatusCode != 200 {
			// real problem: 401/403/5xx
			sawNon404 = true
			Logf("%s[CF KV] HTTP %d for %s: %s", c.logPrefix, resp.StatusCode, key, truncStr(string(body), 100))
			lastErr = fmt.Errorf("KV API %d", resp.StatusCode)
			continue
		}
		var data struct {
			Code string `json:"code"`
			Body string `json:"body"`
		}
		if json.Unmarshal(body, &data) == nil && (data.Code != "" || data.Body != "") {
			return kvResult{Code: data.Code, Body: data.Body}, nil
		}
		raw := strings.TrimSpace(string(body))
		if raw != "" {
			return kvResult{Body: raw}, nil
		}
		// empty 200 — try dual form
		lastErr = nil
	}
	if lastErr != nil {
		return kvResult{}, lastErr
	}
	if sawNon404 {
		return kvResult{}, fmt.Errorf("kv miss")
	}
	return kvResult{}, nil
}

// readWorkerOTP polls Worker GET /api/otp — returns found:false as soft miss (HTTP 200), no 404 spam.
func (c *CFWorkerEmail) readWorkerOTP(apiURL string) (kvResult, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return kvResult{}, err
	}
	req.Header.Set("User-Agent", cfWorkerHTTPUserAgent)
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.session.Do(req)
	if err != nil {
		return kvResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		Logf("%s[CF Worker] /api/otp HTTP %d (检查 api_key): %s", c.logPrefix, resp.StatusCode, truncStr(string(body), 80))
		return kvResult{}, fmt.Errorf("worker otp %d", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return kvResult{}, fmt.Errorf("worker otp %d", resp.StatusCode)
	}
	var result struct {
		Found bool   `json:"found"`
		Code  string `json:"code"`
		Body  string `json:"body"`
	}
	if json.Unmarshal(body, &result) != nil {
		return kvResult{}, fmt.Errorf("worker otp bad json")
	}
	if !result.Found {
		return kvResult{}, nil
	}
	return kvResult{Code: result.Code, Body: result.Body}, nil
}

func (c *CFWorkerEmail) extractOTPCode(src kvResult) string {
	code := strings.TrimSpace(src.Code)
	if c.codeExtractor != nil {
		// Prefer the structured Worker/KV "code" field. Body is a fallback only —
		// scanning body first can false-match unrelated XXX-XXX / digit runs and
		// discard a valid structured code when paired with LoadAndDelete.
		extracted := ""
		if code != "" {
			extracted = c.codeExtractor(code)
		}
		if extracted == "" && src.Body != "" {
			extracted = c.codeExtractor(src.Body)
		}
		if extracted != "" {
			return extracted
		}
		// extractor rejected — treat as miss (keep waiting; caller must not drop cache)
		return ""
	}
	return code
}

// workerOTPAPIURLs builds /api/otp URLs for address dual-forms (root <-> edu.root).
func (c *CFWorkerEmail) workerOTPAPIURLs() []string {
	var urls []string
	for _, key := range otpEmailAliasKeys(c.address) {
		u := fmt.Sprintf("%s/api/otp?email=%s", c.workerURL, url.QueryEscape(key))
		if c.apiKey != "" {
			u += "&key=" + url.QueryEscape(c.apiKey)
		}
		urls = append(urls, u)
	}
	return urls
}

func (c *CFWorkerEmail) WaitForCode(timeout, interval time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	Logf("%s[CF Worker] 等待邮箱验证码 邮箱=%s (本地快速方案: 推送快取→5s后远端回退)", c.logPrefix, c.address)

	cacheKey := strings.ToLower(c.address)
	defer releaseOTPPending(cacheKey)
	apiURLs := c.workerOTPAPIURLs()
	startTime := time.Now()
	deadline := startTime.Add(timeout)
	// 远端回退提前到 2s：Worker 在云端无法回调本地 localhost（推送通道
	// 通常不可达），邮件实际秒到 KV——5s 是纯浪费，2s 后直读 KV 即可。
	fallbackAfter := 2 * time.Second
	fallbackInterval := 3 * time.Second
	var lastFallback time.Time
	attempt := 0

	for time.Now().Before(deadline) {
		if isCancelled(c.cancel) {
			return "", fmt.Errorf("cancelled")
		}
		attempt++
		elapsed := time.Since(startTime)

		// 本地程序的快速路径：每 300ms 只查内存推送缓存，不发远端请求。
		if entry, ok := loadOTPCacheEntry(cacheKey); ok {
			code := c.extractOTPCode(kvResult{Code: entry.Code, Body: entry.Body})
			if code != "" {
				deleteOTPCacheEntry(cacheKey)
				Logf("%s[CF Worker] 推送命中验证码: %s (%.1fs)", c.logPrefix, code, elapsed.Seconds())
				return code, nil
			}
		}

		// 与本地实现一致：5 秒无推送才启用远端回退；有完整 KV
		// 凭据时直读 KV，否则请求 Worker /api/otp。两条不会同时打。
		if elapsed >= fallbackAfter && time.Since(lastFallback) >= fallbackInterval {
			lastFallback = time.Now()
			if c.useKVRestAPI() {
				if res, err := c.readKVDirect(); err == nil {
					if code := c.extractOTPCode(res); code != "" {
						Logf("%s[CF Worker] KV 直读命中验证码: %s (%.1fs)", c.logPrefix, code, elapsed.Seconds())
						return code, nil
					}
				}
			} else {
				for _, apiURL := range apiURLs {
					res, err := c.readWorkerOTP(apiURL)
					if err != nil {
						continue
					}
					if code := c.extractOTPCode(res); code != "" {
						Logf("%s[CF Worker] Worker API 命中验证码: %s (%.1fs)", c.logPrefix, code, elapsed.Seconds())
						return code, nil
					}
				}
			}
		}

		if attempt%20 == 0 {
			Logf("%s[CF Worker] 等待收信中… elapsed=%.0fs email=%s", c.logPrefix, elapsed.Seconds(), c.address)
		}
		if sleepWithCancel(300*time.Millisecond, c.cancel) {
			return "", fmt.Errorf("cancelled")
		}
	}
	return "", fmt.Errorf("CF Worker 等待验证码超时 (%v) — 邮件未写入推送缓存/KV/Worker", timeout)
}

func (c *CFWorkerEmail) ResetForRetry() error { return nil }

func setEmailLogPrefix(svcs []EmailService, prefix string) {
	for _, svc := range svcs {
		if lp, ok := svc.(interface{ SetLogPrefix(string) }); ok {
			lp.SetLogPrefix(prefix)
		}
	}
}

// LoadOutlookAccounts loads accounts from a file (email----password----client_id----refresh_token per line).
func LoadOutlookAccounts(filePath, proxyURL string) []EmailService {
	data, err := readFileContent(filePath)
	if err != nil {
		Logf("[账号文件] 读取失败: %v", err)
		return nil
	}
	var accounts []EmailService
	for lineNum, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) < 4 {
			Logf("[账号文件] 第 %d 行格式错误，跳过", lineNum+1)
			continue
		}
		accounts = append(accounts, NewOutlookEmail(parts[0], parts[1], parts[2], parts[3], proxyURL))
	}
	Logf("[账号文件] 加载 %d 个 Outlook 账号", len(accounts))
	return accounts
}

func readFileContent(path string) ([]byte, error) {
	return os.ReadFile(path)
}
