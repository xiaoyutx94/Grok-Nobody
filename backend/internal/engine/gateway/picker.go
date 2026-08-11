package gateway

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/umbraforge/desktop/internal/engine"
)

// 账号选择与健康管理：分组内账号池 → 轮询 + 随机抖动；
// 单账号并发 ≤ MaxAccountConcurrency；429 → 冷却 30min；403 → 永久标记。
// 语义对齐 sub2api 封号研判：429=量（并发/重试风暴），403=质（身份/IP）。

const (
	// MaxAccountConcurrency 单账号同时转发上限（官方 CLI 单会话串行）。
	MaxAccountConcurrency = 2
	// RateLimitCooldown 429 冷却时长。
	RateLimitCooldown = 30 * time.Minute
	// NetworkCooldown 网络类错误冷却时长。
	NetworkCooldown = 5 * time.Minute
	// PermanentlyForbidden 403 永久封禁标记（账号不再参与路由）。
	PermanentlyForbidden = "grok_forbidden"
)

// AccountPicker 分组账号选择器。
type AccountPicker struct {
	mu      sync.Mutex
	accts   *engine.AccountService
	running map[string]int          // accountID → 当前并发
	cooldown map[string]time.Time   // accountID → 冷却截止
	forbidden map[string]bool       // accountID → 403 永久封禁
	roundRobin int
}

func NewAccountPicker(accts *engine.AccountService) *AccountPicker {
	return &AccountPicker{
		accts:     accts,
		running:   map[string]int{},
		cooldown:  map[string]time.Time{},
		forbidden: map[string]bool{},
	}
}

// AccountEntry 选中结果。
type AccountEntry struct {
	Account engine.Account
	// Proxy 该账号本次请求出口（分组绑定代理轮询结果；空 = 直连）。
	Proxy string
}

// Pick 从分组中选择一个可用账号；无可选账号返回错误。
// groupID 为 AllAccountsGroupID（__all__）时从全部账号池选择（不做分组约束）。
// proxies 为分组绑定的代理列表（空 = 直连），每次调用轮询取一个出口。
func (p *AccountPicker) Pick(ctx context.Context, groupID string, proxies []string) (AccountEntry, error) {
	list, err := p.accts.List(ctx)
	if err != nil {
		return AccountEntry{}, err
	}

	now := time.Now()
	candidates := make([]engine.Account, 0, 4)
	all := groupID == AllAccountsGroupID
	p.mu.Lock()
	for i := range list {
		acc := list[i]
		if !all && acc.GroupID != groupID {
			continue
		}
		if strings.TrimSpace(acc.AccessToken) == "" {
			continue // 无 OAuth 凭证不可用
		}
		// 落库健康状态（与内存状态合并）：封禁 / 限流中 / 临时不可调度一律跳过
		if acc.BannedAt != "" {
			continue
		}
		if until, err := time.Parse(time.RFC3339, acc.RateLimitResetAt); err == nil && now.Before(until) {
			continue
		}
		if until, err := time.Parse(time.RFC3339, acc.TempUnschedulable); err == nil && now.Before(until) {
			continue
		}
		if p.forbidden[acc.ID] {
			continue // 403 永久封禁（内存）
		}
		if until, bad := p.cooldown[acc.ID]; bad && now.Before(until) {
			continue
		}
		if p.running[acc.ID] >= MaxAccountConcurrency {
			continue // 单账号并发上限
		}
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		p.mu.Unlock()
		return AccountEntry{}, errNoAccount(groupID)
	}

	// 轮询 + 随机抖动：起点随机，按 roundRobin 顺序取，避免固定顺序抢同一个账号。
	start := p.roundRobin % len(candidates)
	p.roundRobin++
	acc := candidates[start]

	p.running[acc.ID]++
	p.mu.Unlock()

	proxy := pickProxy(proxies, acc.Proxy)
	return AccountEntry{Account: acc, Proxy: proxy}, nil
}

// Release 请求结束释放账号并发槽（defer 调用）。
func (p *AccountPicker) Release(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running[accountID] > 0 {
		p.running[accountID]--
	}
}

// RecordFailure 记录转发失败并决定是否冷却（内存 + 落库持久化，重启保留）。
//   - 403 → 永久封禁（grok 风控「质」问题，账号不可再用）→ 写 BannedAt
//   - 429 → 冷却 30min（量问题）→ 写 RateLimitResetAt
//   - 其他网络错误 → 冷却 5min → 写 TempUnschedulable
func (p *AccountPicker) RecordFailure(accountID string, statusCode int, errMsg string) {
	p.mu.Lock()
	var persist *engine.AccountPatch
	switch {
	case statusCode == 403 || strings.Contains(strings.ToLower(errMsg), "forbidden"):
		p.forbidden[accountID] = true
		delete(p.cooldown, accountID)
		now := time.Now().UTC().Format(time.RFC3339)
		reason := "上游 403 风控封禁"
		persist = &engine.AccountPatch{BannedAt: &now, BannedReason: &reason}
	case statusCode == 429 || strings.Contains(strings.ToLower(errMsg), "rate limit"):
		until := time.Now().Add(RateLimitCooldown)
		p.cooldown[accountID] = until
		untilStr := until.UTC().Format(time.RFC3339)
		persist = &engine.AccountPatch{RateLimitResetAt: &untilStr}
	case errMsg != "":
		until := time.Now().Add(NetworkCooldown)
		p.cooldown[accountID] = until
		untilStr := until.UTC().Format(time.RFC3339)
		persist = &engine.AccountPatch{TempUnschedulable: &untilStr}
	}
	p.mu.Unlock()
	if persist != nil {
		// 异步落库（不阻塞选号）；JSON 写失败仅日志
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := p.accts.Update(ctx, accountID, *persist); err != nil {
			log.Printf("[picker] 健康状态落库失败 %s: %v", accountID, err)
		}
	}
}

// Snapshot 状态快照（前端展示：可用数/冷却数/封禁数）。
type Snapshot struct {
	Total        int `json:"total"`
	Available    int `json:"available"`
	Cooldown     int `json:"cooldown"`
	Forbidden    int `json:"forbidden"`
}

// Snapshot 按分组统计账号池健康度。
func (p *AccountPicker) Snapshot(ctx context.Context, groupID string) (Snapshot, error) {
	list, err := p.accts.List(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	now := time.Now()
	var snap Snapshot
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range list {
		acc := list[i]
		if acc.GroupID != groupID || strings.TrimSpace(acc.AccessToken) == "" {
			continue
		}
		snap.Total++
		switch {
		case p.forbidden[acc.ID]:
			snap.Forbidden++
		case p.cooldown[acc.ID].After(now):
			snap.Cooldown++
		default:
			snap.Available++
		}
	}
	return snap, nil
}

// pickProxy 选择出口代理：优先分组绑定列表（轮询），空则回退账号自带代理，再空则直连。
func pickProxy(groupProxies []string, accountProxy string) string {
	effective := groupProxies
	if len(effective) == 0 {
		// 分组未绑代理：账号注册时带的代理仅作为直连出口提示（不强制使用，
		// 桌面本机直连 grok 官方更稳；账号代理是注册出口，非网关出口）。
		return ""
	}
	// 每次调用轮询下一个（多账号请求分散到不同代理）
	idx := rand.Intn(len(effective))
	return effective[idx]
}

type noAccountErr struct{ group string }

func (e *noAccountErr) Error() string {
	return "分组无可用账号: " + e.group + "（需要 OAuth 凭证且未冷却/未封禁）"
}

func errNoAccount(group string) error { return &noAccountErr{group: group} }
