package engine

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/umbraforge/desktop/internal/pkg/grokregister"
)

// ── iCloud 本地邮箱池 EmailService ──
//
// 注册链路 email_mode=icloud_api 的默认实现：从本地池（已同步的平台邮箱）
// 领取邮箱（支持按账号过滤），等待验证码时轮询该邮箱的取码 API。
// 池空时回退平台 claim（自动取号）。
//
// 相比直接 claim 的优势：注册页能按账号选择邮箱来源，且不依赖
// 平台邮箱池的剩余量（本地池是平台全部邮箱的快照）。

type iCloudPoolEmail struct {
	gate       *IcloudGateway
	baseURL    string
	apiKey     string
	project    string
	accountIDs []string
	email      string
	apiURL     string
	after      time.Time
	cancel     *grokregister.SafeCancel
}

// NewICloudPoolEmail 构造池模式邮箱服务。
func NewICloudPoolEmail(gate *IcloudGateway, baseURL, apiKey, project string, accountIDs []string) *iCloudPoolEmail {
	return &iCloudPoolEmail{
		gate:       gate,
		baseURL:    baseURL,
		apiKey:     apiKey,
		project:    project,
		accountIDs: accountIDs,
		after:      time.Now(),
	}
}

func (e *iCloudPoolEmail) SetCancel(ch *grokregister.SafeCancel) { e.cancel = ch }
func (e *iCloudPoolEmail) SetLogPrefix(string)                    {}
func (e *iCloudPoolEmail) SetCodeExtractor(func(string) string)   {}

func (e *iCloudPoolEmail) Address() string { return e.email }

// Create 从本地池随机领取邮箱。
// 池空时：① 用已登录的 Apple 账号自动创建随机子邮箱（每账号 1 个）再领；
// ② 仍失败则回退平台 claim 取号。注册全程无需手动创建子邮箱。
func (e *iCloudPoolEmail) Create() (string, error) {
	// 1) 本地池随机领取（按账号过滤）
	if e.gate != nil {
		if ent := e.gate.ClaimFromPool(e.accountIDs); ent != nil {
			e.email = ent.Email
			e.apiURL = ent.APIURL
			e.after = time.Now()
			return e.email, nil
		}
	}
	// 2) 池空：自动创建随机子邮箱（用已登录 Apple 账号，按账号过滤）
	if e.gate != nil {
		sel := strings.Join(e.accountIDs, ",")
		created, err := e.gate.CreateMailboxes(context.Background(), e.baseURL, sel, 1)
		// 平台 2 分钟冷却（并发创建易撞）：解析冷却秒数等待后重试一次
		if err != nil && strings.Contains(err.Error(), "冷却") && !isAppleRateLimited(err.Error()) {
			if secs := cooldownSeconds(err.Error()); secs > 0 {
				time.Sleep(time.Duration(secs+2) * time.Second)
				created, err = e.gate.CreateMailboxes(context.Background(), e.baseURL, sel, 1)
			}
		}
		if err != nil {
			if isAppleRateLimited(err.Error()) {
				return "", fmt.Errorf("iCloud 自动创建子邮箱失败: %v（Apple 对 Hide My Email 创建有速率限制：\"reached the limit ... right now\"，建议等待约 1 小时后重试，或降低注册并发/减少短时间创建次数）", err)
			}
			// 配置了 API Key 且无内置服务 → 回退 claim（外部平台场景）；否则报出真实原因
			if e.apiKey != "" {
				return e.claimFallback()
			}
			return "", fmt.Errorf("iCloud 自动创建子邮箱失败: %w", err)
		}
		if created > 0 {
			if accs, aerr := e.gate.Accounts(context.Background(), e.baseURL); aerr == nil {
				if _, serr := e.gate.SyncPool(context.Background(), e.baseURL, accs); serr == nil {
					if ent := e.gate.ClaimFromPool(e.accountIDs); ent != nil {
						e.email = ent.Email
						e.apiURL = ent.APIURL
						e.after = time.Now()
						return e.email, nil
					}
				}
			}
		}
	}
	// 3) 回退平台 claim（自动取号）
	return e.claimFallback()
}

// claimFallback 平台 claim 自动取号（需要全局 API Key）。
func (e *iCloudPoolEmail) claimFallback() (string, error) {
	svc := grokregister.NewICloudAPIEmail(e.baseURL, e.apiKey, e.project)
	svc.SetCancel(e.cancel)
	addr, err := svc.Create()
	if err != nil {
		return "", err
	}
	e.email = addr
	e.apiURL = svc.APIURL()
	e.after = time.Now()
	return e.email, nil
}

// WaitForCode 轮询取码 API。
func (e *iCloudPoolEmail) WaitForCode(timeout, interval time.Duration) (string, error) {
	if e.email == "" {
		return "", fmt.Errorf("iCloud 邮箱未创建")
	}
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	lastErr := ""
	for time.Now().Before(deadline) {
		if e.cancel != nil && e.cancel.IsClosed() {
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

func (e *iCloudPoolEmail) fetchCode() (string, error) {
	u := e.apiURL
	if u == "" {
		u = fmt.Sprintf("%s/api/v1/mailboxes/%s/code?key=%s",
			e.baseURL, url.PathEscape(e.email), url.QueryEscape(e.apiKey))
	}
	return grokregister.FetchICloudCode(u, e.apiKey, e.after.Format(time.RFC3339), e.project)
}

// cooldownSeconds 从平台冷却错误信息里解析等待秒数
// （"iCloud 创建上限冷却中，请约 117 秒后重试"）。
func cooldownSeconds(msg string) int {
	re := regexp.MustCompile(`(\d+)\s*秒`)
	m := re.FindStringSubmatch(msg)
	if len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

// isAppleRateLimited 判断是否为 Apple 侧 HME 创建速率限制
// （"reached the limit of addresses ... right now ... try again later"）：
// 平台冷却（分钟级）可以等待重试；Apple 速率窗口（小时级）等待无意义，
// 直接报错提示，避免无效循环重试。
func isAppleRateLimited(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "reached the limit") ||
		strings.Contains(low, "right now") ||
		strings.Contains(low, "limit of addresses")
}

// ResetForRetry 释放邮箱回池（可重新领取），下次 Create 换新邮箱。
func (e *iCloudPoolEmail) ResetForRetry() error {
	if e.email != "" && e.gate != nil {
		e.gate.ReleasePoolEmail(e.email)
	}
	e.email = ""
	e.apiURL = ""
	e.after = time.Now()
	return nil
}
