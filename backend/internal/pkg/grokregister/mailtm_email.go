package grokregister

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var mailtmSixDigitRegex = regexp.MustCompile(`(?:^|\D)(\d{6})(?:\D|$)`)

// MailTMEmail implements EmailService using the public Mail.tm API.
// Optional codeExtractor overrides extractCode (e.g. Grok's XXX-XXX format).
type MailTMEmail struct {
	client          *HTTPClient
	proxyURL        string
	email           string
	token           string
	password        string
	cancel          *SafeCancel
	logPrefix       string
	codeExtractor   func(string) string
	requireOpenAIIn bool // if true, only accept messages mentioning OpenAI
}

func NewMailTMEmail(proxyURL string) *MailTMEmail {
	return &MailTMEmail{proxyURL: proxyURL}
}

func (m *MailTMEmail) SetCancel(ch *SafeCancel)                { m.cancel = ch }
func (m *MailTMEmail) SetLogPrefix(prefix string)              { m.logPrefix = prefix }
func (m *MailTMEmail) SetCodeExtractor(fn func(string) string) { m.codeExtractor = fn }
func (m *MailTMEmail) SetRequireOpenAIIn(v bool)               { m.requireOpenAIIn = v }

func (m *MailTMEmail) Address() string { return m.email }

func (m *MailTMEmail) Create() (string, error) {
	if m.client != nil {
		m.client.Close()
	}
	m.client = NewHTTPClientWithBrowser(m.proxyURL, m.proxyURL != "", "")
	acc, err := mailtmCreateAccount(m.client)
	if err != nil {
		return "", err
	}
	m.email = acc.Email
	m.password = acc.Password
	m.token = acc.Token
	Logf("%sMail.tm 邮箱: %s", m.logPrefix, m.email)
	return m.email, nil
}

func (m *MailTMEmail) ResetForRetry() error {
	_, err := m.Create()
	return err
}

func (m *MailTMEmail) WaitForCode(timeout, interval time.Duration) (string, error) {
	if m.client == nil || m.token == "" || m.email == "" {
		return "", fmt.Errorf("Mail.tm 邮箱未初始化")
	}
	extract := extractCode
	if m.codeExtractor != nil {
		extract = m.codeExtractor
	}

	seenIDs := make(map[string]bool)
	maxRetries := int(timeout / interval)
	if maxRetries < 1 {
		maxRetries = 1
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if isCancelled(m.cancel) {
			return "", fmt.Errorf("cancelled")
		}

		resp, err := m.client.Get(mailtmBase+"/messages", map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + m.token,
		})
		if err != nil || resp.StatusCode != 200 {
			if sleepWithCancel(interval, m.cancel) {
				return "", fmt.Errorf("cancelled")
			}
			continue
		}

		type msgItem struct {
			ID string `json:"id"`
		}
		var messages []msgItem
		if err := json.Unmarshal(resp.Body, &messages); err != nil {
			var obj struct {
				Member   []msgItem `json:"hydra:member"`
				Messages []msgItem `json:"messages"`
			}
			json.Unmarshal(resp.Body, &obj)
			if len(obj.Member) > 0 {
				messages = obj.Member
			} else {
				messages = obj.Messages
			}
		}

		for _, msg := range messages {
			if msg.ID == "" || seenIDs[msg.ID] {
				continue
			}
			seenIDs[msg.ID] = true

			msgResp, err := m.client.Get(mailtmBase+"/messages/"+msg.ID, map[string]string{
				"Accept":        "application/json",
				"Authorization": "Bearer " + m.token,
			})
			if err != nil || msgResp.StatusCode != 200 {
				continue
			}

			var mailData struct {
				From struct {
					Address string `json:"address"`
				} `json:"from"`
				Subject string          `json:"subject"`
				Intro   string          `json:"intro"`
				Text    string          `json:"text"`
				HTML    json.RawMessage `json:"html"`
			}
			if err := json.Unmarshal(msgResp.Body, &mailData); err != nil {
				continue
			}

			var htmlStr string
			var htmlArr []string
			if json.Unmarshal(mailData.HTML, &htmlArr) == nil {
				htmlStr = strings.Join(htmlArr, "\n")
			} else {
				var htmlSingle string
				if json.Unmarshal(mailData.HTML, &htmlSingle) == nil {
					htmlStr = htmlSingle
				} else {
					htmlStr = string(mailData.HTML)
				}
			}
			content := strings.Join([]string{mailData.Subject, mailData.Intro, mailData.Text, htmlStr}, "\n")
			sender := strings.ToLower(mailData.From.Address)
			lowerContent := strings.ToLower(content)

			if m.requireOpenAIIn && !strings.Contains(sender, "openai") && !strings.Contains(lowerContent, "openai") {
				continue
			}

			if code := extract(content); code != "" {
				Logf("%sMail.tm 验证码: %s", m.logPrefix, code)
				return code, nil
			}
			if m.codeExtractor == nil {
				if sub := mailtmSixDigitRegex.FindStringSubmatch(content); len(sub) >= 2 {
					Logf("%sMail.tm 验证码(回退): %s", m.logPrefix, sub[1])
					return sub[1], nil
				}
			}
		}

		if sleepWithCancel(interval, m.cancel) {
			return "", fmt.Errorf("cancelled")
		}
	}

	return "", fmt.Errorf("等待验证码超时 (%v)", timeout)
}
