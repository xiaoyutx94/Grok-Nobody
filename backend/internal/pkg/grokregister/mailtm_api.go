package grokregister

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ── Mail.tm temporary email API ──

const mailtmBase = "https://api.mail.tm"

type mailtmDomain struct {
	Domain    string `json:"domain"`
	IsActive  *bool  `json:"isActive,omitempty"`
	IsPrivate *bool  `json:"isPrivate,omitempty"`
}

func (d mailtmDomain) active() bool {
	if d.IsActive == nil {
		return true // default true like Python
	}
	return *d.IsActive
}

func (d mailtmDomain) private() bool {
	if d.IsPrivate == nil {
		return false
	}
	return *d.IsPrivate
}

type mailtmAccount struct {
	Email    string
	Password string
	Token    string
}

func mailtmGetDomains(client *HTTPClient) ([]string, error) {
	resp, err := client.Get(mailtmBase+"/domains", map[string]string{
		"Accept": "application/json",
	})
	if err != nil {
		return nil, fmt.Errorf("获取 Mail.tm 域名失败: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("获取 Mail.tm 域名失败, 状态码: %d", resp.StatusCode)
	}

	// Parse response - could be array or object with hydra:member
	var domains []string
	addDomains := func(list []mailtmDomain) {
		for _, d := range list {
			if d.Domain != "" && d.active() && !d.private() {
				domains = append(domains, d.Domain)
			}
		}
	}

	// Try as plain array first
	var rawList []mailtmDomain
	if err := json.Unmarshal(resp.Body, &rawList); err == nil {
		addDomains(rawList)
		return domains, nil
	}

	// Try as object with hydra:member or items
	var obj struct {
		Member []mailtmDomain `json:"hydra:member"`
		Items  []mailtmDomain `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &obj); err == nil {
		if len(obj.Member) > 0 {
			addDomains(obj.Member)
		} else {
			addDomains(obj.Items)
		}
	}
	return domains, nil
}

func mailtmCreateAccount(client *HTTPClient) (*mailtmAccount, error) {
	domains, err := mailtmGetDomains(client)
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return nil, fmt.Errorf("Mail.tm 没有可用域名")
	}

	for i := 0; i < 5; i++ {
		domain := domains[i%len(domains)]
		local := "oc" + randomHex(5)
		email := local + "@" + domain
		password := randomURLSafe(18)

		createResp, err := client.PostJSON(mailtmBase+"/accounts", map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/json",
		}, map[string]string{"address": email, "password": password})
		if err != nil || (createResp.StatusCode != 200 && createResp.StatusCode != 201) {
			continue
		}

		tokenResp, err := client.PostJSON(mailtmBase+"/token", map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/json",
		}, map[string]string{"address": email, "password": password})
		if err != nil || tokenResp.StatusCode != 200 {
			continue
		}

		var tokenData struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(tokenResp.Body, &tokenData); err != nil || tokenData.Token == "" {
			continue
		}

		return &mailtmAccount{
			Email:    email,
			Password: password,
			Token:    tokenData.Token,
		}, nil
	}

	return nil, fmt.Errorf("Mail.tm 邮箱创建失败 (5次重试)")
}

func mailtmGetOTP(client *HTTPClient, token, email string, cancel *SafeCancel) (string, error) {
	otpRegex := regexp.MustCompile(`(?:^|\D)(\d{6})(?:\D|$)`)
	seenIDs := make(map[string]bool)

	Logf("[*] 正在等待邮箱 %s 的验证码...", email)

	for attempt := 0; attempt < 40; attempt++ {
		if isCancelled(cancel) {
			return "", fmt.Errorf("cancelled")
		}

		resp, err := client.Get(mailtmBase+"/messages", map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + token,
		})
		if err != nil || resp.StatusCode != 200 {
			sleepWithCancel(3*time.Second, cancel)
			continue
		}

		// Parse messages list
		type msgItem struct {
			ID string `json:"id"`
		}
		var messages []msgItem
		// Try array first
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

			msgResp, err := client.Get(mailtmBase+"/messages/"+msg.ID, map[string]string{
				"Accept":        "application/json",
				"Authorization": "Bearer " + token,
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

			// Parse HTML: could be string, array of strings, or null
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

			if !strings.Contains(sender, "openai") && !strings.Contains(strings.ToLower(content), "openai") {
				continue
			}

			m := otpRegex.FindStringSubmatch(content)
			if len(m) >= 2 {
				Logf("[*] 抓到验证码: %s", m[1])
				return m[1], nil
			}
		}

		sleepWithCancel(3*time.Second, cancel)
	}

	return "", fmt.Errorf("等待验证码超时 (120s)")
}
