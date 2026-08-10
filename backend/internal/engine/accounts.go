package engine

import (
        "bufio"
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        neturl "net/url"
        "sort"
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"
        "github.com/umbraforge/desktop/internal/pkg/grokregister"
        "github.com/umbraforge/desktop/internal/pkg/proxyurl"
        "github.com/umbraforge/desktop/internal/pkg/xai"
        "github.com/umbraforge/desktop/internal/store"
)

const KeyAccounts = "grok_register_accounts"
const KeyProxyPool = "grok_register_proxy_pool"

// Account is a successful registration retained with full credentials.
type Account struct {
        ID           string `json:"id"`
        Email        string `json:"email"`
        Password     string `json:"password,omitempty"`
        SSO          string `json:"sso,omitempty"`
        SSORW        string `json:"sso_rw,omitempty"`
        AccessToken  string `json:"access_token,omitempty"`
        RefreshToken string `json:"refresh_token,omitempty"`
        IDToken      string `json:"id_token,omitempty"`
        TokenType    string `json:"token_type,omitempty"`
        ExpiresIn    any    `json:"expires_in,omitempty"`
        OAuthScope   string `json:"oauth_scope,omitempty"`
        BaseURL      string `json:"base_url,omitempty"`
        Browser      string `json:"browser,omitempty"`
        OS           string `json:"os,omitempty"`
        Proxy        string `json:"proxy,omitempty"`
        Status       string `json:"status"`
        Imported     bool   `json:"imported"`
        // GroupID/GroupName 网关分组归属（空 = 未分组，不参与网关账号路由）。
        GroupID   string `json:"group_id,omitempty"`
        GroupName string `json:"group_name,omitempty"`
        Note      string `json:"note,omitempty"`
        CreatedAt    string `json:"created_at"`
        UpdatedAt    string `json:"updated_at"`
        Raw          map[string]any `json:"raw,omitempty"`

        // 单账号测试留痕（测试对话 / 凭证校验后写回，列表直接展示健康度）
        LastTestAt     string `json:"last_test_at,omitempty"`
        LastTestStatus string `json:"last_test_status,omitempty"` // ok | fail
        LastTestError  string `json:"last_test_error,omitempty"`
        LastTestMs     int64  `json:"last_test_ms,omitempty"`
        LastTestModel  string `json:"last_test_model,omitempty"`
}

type ProxyEntry struct {
        URL     string `json:"url"`
        Note    string `json:"note,omitempty"`
        Enabled bool   `json:"enabled"`
        // Source 条目来源。空 = 用户手动添加；ProxySourceWarp = WARP 实例自动并入。
        //
        // 需要这个字段是因为 WARP 出口的生命周期由容器决定，不该被代理池页删除：
        // 删了条目容器还在跑，用户会以为已经移除。前端据此禁用删除并打标记，
        // 真正的移除走 WARP 页删容器（会同步清掉这里的条目）。
        Source string `json:"source,omitempty"`
}

// ProxySourceWarp 标记「该条目由 WARP 实例自动并入」。
const ProxySourceWarp = "warp"

type ProxyPool struct {
        // URLs: 兼容旧数据 + 注册引擎消费（仅启用条目）。新数据以 Entries 为准。
        URLs    []string     `json:"urls"`
        Entries []ProxyEntry `json:"entries,omitempty"`
}

// ProxySettings holds independent proxy routing for registration / captcha / import.
type ProxySettings struct {
        // Registration pool (Grok register egress)
        RegisterURLs []string `json:"register_urls"`
        // Captcha free-solver egress only
        CaptchaMode string   `json:"captcha_mode"` // direct|registration|global|selected
        CaptchaURLs []string `json:"captcha_urls"`
        // Import / convert egress only
        ImportMode string   `json:"import_mode"` // direct|registration|global|selected
        ImportURLs []string `json:"import_urls"`
}

const KeyProxySettings = "grok_register_proxy_settings"

type AccountService struct {
        store *store.JSONStore
        mu    sync.Mutex
        // importProg 入库转换的实时进度（见 import_progress.go）。
        // 转换是分钟级操作，必须能被前端轮询，否则界面只能干等。
        importProg *importProgressTracker
}

func NewAccountService(st *store.JSONStore) *AccountService {
        return &AccountService{store: st, importProg: &importProgressTracker{}}
}

func (s *AccountService) List(ctx context.Context) ([]Account, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.load(ctx)
}

func (s *AccountService) load(ctx context.Context) ([]Account, error) {
        raw, err := s.store.GetValue(ctx, KeyAccounts)
        if err != nil {
                return nil, err
        }
        if strings.TrimSpace(raw) == "" {
                return []Account{}, nil
        }
        var list []Account
        if err := json.Unmarshal([]byte(raw), &list); err != nil {
                return nil, err
        }
        sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
        return list, nil
}

func (s *AccountService) save(ctx context.Context, list []Account) error {
        b, err := json.Marshal(list)
        if err != nil {
                return err
        }
        return s.store.Set(ctx, KeyAccounts, string(b))
}

func strFrom(m map[string]any, keys ...string) string {
        for _, k := range keys {
                if v, ok := m[k]; ok {
                        switch t := v.(type) {
                        case string:
                                if strings.TrimSpace(t) != "" {
                                        return t
                                }
                        }
                }
        }
        return ""
}

// UpsertFromResult saves a successful registration result with full secrets.
func (s *AccountService) UpsertFromResult(ctx context.Context, result map[string]any) (Account, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        list, err := s.load(ctx)
        if err != nil {
                return Account{}, err
        }
        now := time.Now().UTC().Format(time.RFC3339)
        email := strFrom(result, "email")
        sso := strFrom(result, "sso", "sso_token", "key")
        importedFlag, hasImportFlag := result["imported"].(bool)
        acc := Account{
                ID:           uuid.NewString(),
                Email:        email,
                Password:     strFrom(result, "password"),
                Imported:     importedFlag,
                SSO:          sso,
                SSORW:        strFrom(result, "sso_rw", "sso-rw"),
                AccessToken:  strFrom(result, "access_token"),
                RefreshToken: strFrom(result, "refresh_token"),
                IDToken:      strFrom(result, "id_token"),
                TokenType:    strFrom(result, "token_type"),
                ExpiresIn:    result["expires_in"],
                OAuthScope:   strFrom(result, "oauth_scope"),
                BaseURL:      strFrom(result, "base_url"),
                Browser:      strFrom(result, "browser"),
                OS:           strFrom(result, "os"),
                Proxy:        strFrom(result, "proxy", "registration_proxy"),
                Status:       strFrom(result, "status"),
                GroupID:      strFrom(result, "group_id"),
                GroupName:    strFrom(result, "group_name"),
                CreatedAt:    now,
                UpdatedAt:    now,
                Raw:          result,
        }
        if acc.Status == "" {
                acc.Status = "success"
        }
        // 注册回填时钉住 CLI 网关：免费账号只有 grok-cli:access，
        // 空 base_url 会让后续调用落到 api.x.ai 被判无额度 402。
        if strings.TrimSpace(acc.BaseURL) == "" {
                acc.BaseURL = xai.DefaultCLIBaseURL
        }
        // dedupe by email+sso
        for i, old := range list {
                if (email != "" && strings.EqualFold(old.Email, email)) || (sso != "" && old.SSO == sso) {
                        acc.ID = old.ID
                        acc.CreatedAt = old.CreatedAt
                        if !hasImportFlag {
                                acc.Imported = old.Imported
                        }
                        acc.Note = old.Note
                        // 保留测试留痕：重复注册/回填不应清掉既有健康度
                        acc.LastTestAt = old.LastTestAt
                        acc.LastTestStatus = old.LastTestStatus
                        acc.LastTestError = old.LastTestError
                        acc.LastTestMs = old.LastTestMs
                        acc.LastTestModel = old.LastTestModel
                        list[i] = acc
                        return acc, s.save(ctx, list)
                }
        }
        list = append(list, acc)
        return acc, s.save(ctx, list)
}

func (s *AccountService) Delete(ctx context.Context, ids []string) (int, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        list, err := s.load(ctx)
        if err != nil {
                return 0, err
        }
        drop := map[string]struct{}{}
        for _, id := range ids {
                drop[id] = struct{}{}
        }
        out := list[:0]
        n := 0
        for _, a := range list {
                if _, ok := drop[a.ID]; ok {
                        n++
                        continue
                }
                out = append(out, a)
        }
        return n, s.save(ctx, out)
}

func (s *AccountService) Clear(ctx context.Context) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.save(ctx, []Account{})
}

// ProxyTestResult 单条代理检测结果。
type ProxyTestResult struct {
        URL       string `json:"url"`
        OK        bool   `json:"ok"`
        IP        string `json:"ip,omitempty"`
        LatencyMs int64  `json:"latency_ms"`
        Error     string `json:"error,omitempty"`
}

// TestProxyPool 并发检测代理可用性（复用注册引擎同一套探测逻辑）。
func TestProxyPool(ctx context.Context, urls []string) []ProxyTestResult {
        results := make([]ProxyTestResult, 0, len(urls))
        if len(urls) == 0 {
                return results
        }
        var wg sync.WaitGroup
        sem := make(chan struct{}, 8)
        mu := sync.Mutex{}
        for _, u := range urls {
                u = strings.TrimSpace(u)
                if u == "" {
                        continue
                }
                wg.Add(1)
                go func(proxyURL string) {
                        defer wg.Done()
                        sem <- struct{}{}
                        defer func() { <-sem }()
                        start := time.Now()
                        ip, ok := grokregister.TestProxyAlive(proxyURL)
                        r := ProxyTestResult{URL: proxyURL, OK: ok, IP: ip, LatencyMs: time.Since(start).Milliseconds()}
                        if !ok {
                                r.Error = "连接/出口检测失败"
                        }
                        mu.Lock()
                        results = append(results, r)
                        mu.Unlock()
                }(u)
        }
        wg.Wait()
        // 保持输入顺序
        order := map[string]ProxyTestResult{}
        for _, r := range results {
                order[r.URL] = r
        }
        ordered := make([]ProxyTestResult, 0, len(urls))
        for _, u := range urls {
                if r, ok := order[strings.TrimSpace(u)]; ok {
                        ordered = append(ordered, r)
                }
        }
        return ordered
}

func (s *AccountService) MarkImported(ctx context.Context, ids []string, imported bool) (int, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        list, err := s.load(ctx)
        if err != nil {
                return 0, err
        }
        set := map[string]struct{}{}
        for _, id := range ids {
                set[id] = struct{}{}
        }
        n := 0
        now := time.Now().UTC().Format(time.RFC3339)
        for i := range list {
                if _, ok := set[list[i].ID]; ok || len(ids) == 0 {
                        list[i].Imported = imported
                        list[i].UpdatedAt = now
                        n++
                }
        }
        return n, s.save(ctx, list)
}

// Export formats:
// - json: full accounts
// - sub2api: {sso_tokens:[]}
// - newapi: newline text type/name/credentials blocks for new-api import style
// - full: email----password----sso----sso_rw----access_token----refresh_token
// - csv: header csv
func (s *AccountService) Export(ctx context.Context, format string, successOnly bool) (string, string, error) {
        list, err := s.List(ctx)
        if err != nil {
                return "", "", err
        }
        rows := make([]Account, 0, len(list))
        for _, a := range list {
                if successOnly && !strings.EqualFold(a.Status, "success") {
                        continue
                }
                rows = append(rows, a)
        }
        switch strings.ToLower(strings.TrimSpace(format)) {
        case "", "json":
                b, err := json.MarshalIndent(rows, "", "  ")
                return string(b), "application/json", err
        case "sub2api":
                // sub2api「导入数据」认的完整授权包（含 OAuth 凭证 + 注册代理）。
                // 旧实现只导 {"sso_tokens":[...]}，缺 proxies/accounts 两个数组，
                // 前端 isValidDataPayload 直接判格式错误。
                b, err := json.MarshalIndent(buildSub2APIBundle(rows, true), "", "  ")
                return string(b), "application/json", err
        case "sub2api-noproxy":
                // 同上，但不带代理（对端自己配出口时用）
                b, err := json.MarshalIndent(buildSub2APIBundle(rows, false), "", "  ")
                return string(b), "application/json", err
        case "sub2api-sso":
                // 旧行为保留：纯 SSO 列表，一行一个，贴进 sub2api 的
                // 「批量添加账号 → SSO」输入框（对端会重新做 SSO→OAuth 转换）
                var b strings.Builder
                for _, a := range rows {
                        if s := strings.TrimSpace(a.SSO); s != "" {
                                b.WriteString(s)
                                b.WriteByte('\n')
                        }
                }
                return b.String(), "text/plain", nil
        case "newapi", "new-api":
                // new-api 渠道数组（xAI type=48）。旧实现导的是自造 NDJSON，
                // new-api 没有任何入口能吃。这里带 header_override 注入 CLI 身份头，
                // 否则 new-api 只发 Authorization: Bearer 会被 CLI 网关判 426。
                channels := buildNewAPIChannels(rows)
                b, err := json.MarshalIndent(channels, "", "  ")
                return string(b), "application/json", err
        case "full", "txt":
                var b strings.Builder
                for _, a := range rows {
                        // complete email----password----sso----sso_rw----access_token----refresh_token
                        b.WriteString(strings.Join([]string{
                                a.Email, a.Password, a.SSO, a.SSORW, a.AccessToken, a.RefreshToken,
                        }, "----"))
                        b.WriteByte('\n')
                }
                return b.String(), "text/plain", nil
        case "csv":
                var b strings.Builder
                b.WriteString("email,password,sso,sso_rw,access_token,refresh_token,status,proxy,created_at\n")
                for _, a := range rows {
                        b.WriteString(fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
                                csvEsc(a.Email), csvEsc(a.Password), csvEsc(a.SSO), csvEsc(a.SSORW),
                                csvEsc(a.AccessToken), csvEsc(a.RefreshToken), csvEsc(a.Status), csvEsc(a.Proxy), csvEsc(a.CreatedAt)))
                }
                return b.String(), "text/csv", nil
        default:
                return "", "", fmt.Errorf("unknown format: %s (json|sub2api|sub2api-noproxy|sub2api-sso|newapi|full|csv)", format)
        }
}

func csvEsc(s string) string {
        if strings.ContainsAny(s, ",\"\n") {
                return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
        }
        return s
}

func cleanURLs(urls []string) []string {
        clean := make([]string, 0, len(urls))
        seen := map[string]struct{}{}
        for _, u := range urls {
                u = strings.TrimSpace(u)
                if u == "" {
                        continue
                }
                if _, ok := seen[u]; ok {
                        continue
                }
                seen[u] = struct{}{}
                clean = append(clean, u)
        }
        return clean
}

func normalizeProxyMode(mode, def string) string {
        switch strings.ToLower(strings.TrimSpace(mode)) {
        case "direct", "registration", "global", "selected":
                return strings.ToLower(strings.TrimSpace(mode))
        default:
                if def == "" {
                        return "direct"
                }
                return def
        }
}

func (s *AccountService) GetProxySettings(ctx context.Context) (ProxySettings, error) {
        raw, err := s.store.GetValue(ctx, KeyProxySettings)
        if err != nil {
                return ProxySettings{}, err
        }
        var ps ProxySettings
        if strings.TrimSpace(raw) != "" {
                _ = json.Unmarshal([]byte(raw), &ps)
        }
        // migrate legacy single pool
        if len(ps.RegisterURLs) == 0 {
                legacy, _ := s.GetProxyPool(ctx)
                ps.RegisterURLs = legacy.URLs
        }
        if ps.RegisterURLs == nil {
                ps.RegisterURLs = []string{}
        }
        if ps.CaptchaURLs == nil {
                ps.CaptchaURLs = []string{}
        }
        if ps.ImportURLs == nil {
                ps.ImportURLs = []string{}
        }
        ps.CaptchaMode = normalizeProxyMode(ps.CaptchaMode, "registration")
        ps.ImportMode = normalizeProxyMode(ps.ImportMode, "direct")
        return ps, nil
}

func (s *AccountService) SaveProxySettings(ctx context.Context, ps ProxySettings) (ProxySettings, error) {
        ps.RegisterURLs = cleanURLs(ps.RegisterURLs)
        ps.CaptchaURLs = cleanURLs(ps.CaptchaURLs)
        ps.ImportURLs = cleanURLs(ps.ImportURLs)
        ps.CaptchaMode = normalizeProxyMode(ps.CaptchaMode, "registration")
        ps.ImportMode = normalizeProxyMode(ps.ImportMode, "direct")
        b, _ := json.Marshal(ps)
        if err := s.store.Set(ctx, KeyProxySettings, string(b)); err != nil {
                return ps, err
        }
        // 同步 legacy key，但**绝不能丢掉 Entries**。
        //
        // 这里原本是 json.Marshal(ProxyPool{URLs: ps.RegisterURLs})，把整个
        // KeyProxyPool 覆写成「只有 URLs」—— 代理池的 entries（URL+备注+启用
        // 状态）连同用户刚添加的几十条代理一起被抹平。
        //
        // 用户实测症状：代理池页添加代理 → 确实落盘（PUT entries=46 → 200）→
        // 切换导航时本页的「出口设置」autosave 触发 SaveProxySettings →
        // 这一行把池子清空 → 切回来一条不剩。而且 RegisterURLs 通常是空的
        //（没单独配注册出口），所以连 urls 也变成 0，看起来就是「什么都没存」。
        //
        // 正确做法：读出现有池子，只更新 URLs 镜像，保留 Entries；
        // RegisterURLs 为空时完全不动 legacy key ——「没单独配注册出口」
        // 不等于「要清空代理池」。
        // 不再镜像到 KeyProxyPool。
        //
        // 这条平行通路是两个 bug 的共同根源：pool.URLs 有两个互相覆盖的写入者
        //（SaveProxyPool 从 Entries 重算、SaveProxySettings 从 RegisterURLs 镜像），
        // 而注册消费的正是 pool.URLs，谁后写谁赢：
        //   1. 覆写时丢掉 Entries → 用户刚加的几十条代理连备注一起消失
        //      （切换导航就触发，见 TestSaveProxySettingsMustNotClobberPool）
        //   2. WARP 往 RegisterURLs 追加的出口被代理池那侧重算抹掉
        //
        // 现在 pool.URLs 只由 SaveProxyPool 从 Entries 派生（单一写入者），
        // WARP 出口以 Source=warp 的条目形式进池（见 proxy_pool_warp.go），
        // 注册出口 = 所有启用条目，两个 bug 一并消失。
        return ps, nil
}

func (s *AccountService) GetProxyPool(ctx context.Context) (ProxyPool, error) {
        // 主存储：KeyProxyPool（含 entries + 备注）。优先读取，避免旧
        // KeyProxySettings 镜像（只有 URL 列表）覆盖掉备注/启用状态。
        raw, err := s.store.GetValue(ctx, KeyProxyPool)
        if err != nil {
                return ProxyPool{}, err
        }
        var p ProxyPool
        if strings.TrimSpace(raw) != "" {
                _ = json.Unmarshal([]byte(raw), &p)
                if p.URLs == nil {
                        p.URLs = []string{}
                }
        } else {
                // 兼容回退：老版本只写过 KeyProxySettings 镜像
                rawSettings, _ := s.store.GetValue(ctx, KeyProxySettings)
                if strings.TrimSpace(rawSettings) != "" {
                        var ps ProxySettings
                        _ = json.Unmarshal([]byte(rawSettings), &ps)
                        if ps.RegisterURLs == nil {
                                ps.RegisterURLs = []string{}
                        }
                        p = ProxyPool{URLs: ps.RegisterURLs}
                } else {
                        return ProxyPool{URLs: []string{}}, nil
                }
        }
        if len(p.Entries) == 0 {
                // 旧数据迁移：纯 URL 列表 → 条目（默认启用）
                for _, u := range p.URLs {
                        p.Entries = append(p.Entries, ProxyEntry{URL: u, Enabled: true})
                }
        } else {
                // 新数据：URLs 派生自启用条目，保持注册引擎消费一致
                var enabled []string
                for _, e := range p.Entries {
                        if e.Enabled && strings.TrimSpace(e.URL) != "" {
                                enabled = append(enabled, strings.TrimSpace(e.URL))
                        }
                }
                p.URLs = enabled
        }
        return p, nil
}

func (s *AccountService) SaveProxyPool(ctx context.Context, p ProxyPool) (ProxyPool, error) {
        if len(p.Entries) > 0 {
                var clean []ProxyEntry
                seen := map[string]struct{}{}
                for _, e := range p.Entries {
                        e.URL = strings.TrimSpace(e.URL)
                        if e.URL == "" {
                                continue
                        }
                        if _, ok := seen[e.URL]; ok {
                                continue
                        }
                        seen[e.URL] = struct{}{}
                        clean = append(clean, e)
                }
                p.Entries = clean
                var enabled []string
                for _, e := range p.Entries {
                        if e.Enabled {
                                enabled = append(enabled, e.URL)
                        }
                }
                p.URLs = enabled
        } else {
                p.URLs = cleanURLs(p.URLs)
        }
        b, _ := json.Marshal(p)
        if err := s.store.Set(ctx, KeyProxyPool, string(b)); err != nil {
                return p, err
        }
        // soft-mirror register_urls into settings without full SaveProxySettings (avoid recursion)
        raw, _ := s.store.GetValue(ctx, KeyProxySettings)
        var ps ProxySettings
        if strings.TrimSpace(raw) != "" {
                _ = json.Unmarshal([]byte(raw), &ps)
        }
        ps.RegisterURLs = p.URLs
        if ps.CaptchaMode == "" {
                ps.CaptchaMode = "registration"
        }
        if ps.ImportMode == "" {
                ps.ImportMode = "direct"
        }
        if ps.CaptchaURLs == nil {
                ps.CaptchaURLs = []string{}
        }
        if ps.ImportURLs == nil {
                ps.ImportURLs = []string{}
        }
        bb, _ := json.Marshal(ps)
        _ = s.store.Set(ctx, KeyProxySettings, string(bb))
        return p, nil
}

// PickImportProxy returns proxy URL for one import job by mode.
func PickImportProxy(mode string, importURLs, registerURLs []string, regProxy string, idx int) string {
        mode = normalizeProxyMode(mode, "direct")
        switch mode {
        case "direct":
                return ""
        case "registration":
                if regProxy != "" {
                        return regProxy
                }
                if len(registerURLs) == 0 {
                        return ""
                }
                if idx < 0 {
                        idx = -idx
                }
                return registerURLs[idx%len(registerURLs)]
        case "selected":
                if len(importURLs) == 0 {
                        return ""
                }
                return importURLs[0]
        case "global":
                if len(importURLs) == 0 {
                        return ""
                }
                if idx < 0 {
                        idx = -idx
                }
                return importURLs[idx%len(importURLs)]
        default:
                return ""
        }
}

// ParseProxyText splits multi-line / comma / semicolon proxy list.
func ParseProxyText(text string) []string {
        text = strings.ReplaceAll(text, "\r\n", "\n")
        text = strings.ReplaceAll(text, ";", "\n")
        text = strings.ReplaceAll(text, ",", "\n")
        var out []string
        for _, line := range strings.Split(text, "\n") {
                line = strings.TrimSpace(line)
                if line != "" {
                        out = append(out, line)
                }
        }
        return out
}

// splitProxyNote 拆分 URL 与备注：支持 "url #备注" 与 "url|备注" 两种写法。
// 备注仅用于展示/管理，不参与注册出口。
func splitProxyNote(line string) (url, note string) {
        line = strings.TrimSpace(line)
        // 分隔符：# 或 |（可带可不带前导空格，如 "url#备注" / "url #备注" / "url|备注"）
        idx := -1
        for i, r := range line {
                if r == '#' || r == '|' {
                        idx = i
                        break
                }
        }
        if idx > 0 && idx < len(line)-1 {
                url = strings.TrimSpace(line[:idx])
                note = strings.TrimSpace(line[idx+1:])
                // 备注可能是 URL 编码（如 #%E6%97%A5%E6%9C%ACZ05）——解码为可读文本
                if dec, err := neturl.PathUnescape(note); err == nil && dec != note {
                        note = dec
                }
                return url, note
        }
        return line, ""
}

// ParseProxyTextEntries 解析代理池文本为条目（URL + 备注 + 默认启用），按 URL 去重。
func ParseProxyTextEntries(text string) []ProxyEntry {
        var out []ProxyEntry
        seen := map[string]struct{}{}
        for _, line := range ParseProxyText(text) {
                u, note := splitProxyNote(line)
                u = strings.TrimSpace(u)
                if u == "" {
                        continue
                }
                if _, ok := seen[u]; ok {
                        continue
                }
                seen[u] = struct{}{}
                out = append(out, ProxyEntry{URL: u, Note: note, Enabled: true})
        }
        return out
}

func truncStr(s string, n int) string {
        r := []rune(s)
        if len(r) <= n {
                return s
        }
        return string(r[:n]) + "…"
}

// DefaultTestPrompt 测试对话默认提示词（用户不填时用它，保证一键可测）。
const DefaultTestPrompt = "用一句话自我介绍，并回复当前模型名称。"

// DefaultTestModel 测试对话默认模型。
const DefaultTestModel = "grok-4.5"

// TestChatOptions 单账号测试对话参数。
type TestChatOptions struct {
	Model string `json:"model"`
	// Prompt 为空时使用 DefaultTestPrompt。
	Prompt string `json:"prompt"`
	// UseProxy 为 true 且账号带注册代理时，用该代理出网（与注册出口一致，规避风控）。
	UseProxy bool `json:"use_proxy"`
	// Stream 为 true 时按增量回调 OnDelta，否则一次性返回。
	Stream bool `json:"stream"`
}

// TestChatResult 单账号测试对话结果。
type TestChatResult struct {
	Reply            string `json:"reply"`
	Model            string `json:"model"`
	LatencyMs        int64  `json:"latency_ms"`
	PromptTokens     int64  `json:"prompt_tokens,omitempty"`
	CompletionTokens int64  `json:"completion_tokens,omitempty"`
	TotalTokens      int64  `json:"total_tokens,omitempty"`
	// ProxyUsed 已脱敏（去凭据），仅用于展示实际出口。
	ProxyUsed string `json:"proxy_used,omitempty"`
}

// accountBaseURL 返回账号有效的 OpenAI 兼容 base。
//
// 缺省必须是 CLI 网关 cli-chat-proxy.grok.com，不能是 api.x.ai：
// SSO 换来的免费账号拿到的是 grok-cli:access 授权，走 api.x.ai 会被判
// 「无 API 额度」直接 402（personal-team-blocked:spending-limit）。
// 与 sub2api 一致（account.go: Grok OAuth 账号默认 xai.DefaultCLIBaseURL）。
func accountBaseURL(acc *Account) string {
	base := strings.TrimSpace(acc.BaseURL)
	if base == "" {
		base = xai.DefaultCLIBaseURL
	}
	return strings.TrimSuffix(base, "/")
}

// applyGrokCLIHeaders 补齐 CLI 网关要求的客户端身份头。
// cli-chat-proxy 只认 grok-cli 身份，缺这几个头会被拒。
func applyGrokCLIHeaders(req *http.Request, accessToken string) {
	if req == nil {
		return
	}
	if token := strings.TrimSpace(accessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(xai.CLITokenAuthHeader, xai.CLITokenAuthValue)
	req.Header.Set(xai.CLIClientVersionHeader, xai.CLIClientVersion)
	req.Header.Set("User-Agent", xai.CLIUserAgentString())
}

// newAccountHTTPClient 按账号代理构造 http.Client。
// useProxy 且账号代理有效时走代理；代理无效直接报错（fail-fast，不静默回退直连以免 IP 关联）。
func newAccountHTTPClient(acc *Account, useProxy bool, timeout time.Duration) (*http.Client, string, error) {
	client := &http.Client{Timeout: timeout}
	if !useProxy {
		return client, "", nil
	}
	trimmed, parsed, err := proxyurl.Parse(acc.Proxy)
	if err != nil {
		return nil, "", fmt.Errorf("账号代理无效: %v", err)
	}
	if trimmed == "" {
		return client, "", nil
	}
	client.Transport = &http.Transport{
		Proxy:               http.ProxyURL(parsed),
		TLSHandshakeTimeout: 20 * time.Second,
	}
	return client, parsed.Redacted(), nil
}

// findAccount 在列表中定位账号（返回下标，-1 表示不存在）。
func findAccount(list []Account, id string) int {
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

// Get 返回单个账号（含完整凭据，桌面本地用）。
func (s *AccountService) Get(ctx context.Context, id string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load(ctx)
	if err != nil {
		return Account{}, err
	}
	idx := findAccount(list, id)
	if idx < 0 {
		return Account{}, fmt.Errorf("账号不存在")
	}
	return list[idx], nil
}

// recordTest 写回单账号测试留痕（成功/失败都记，列表可见健康度）。
func (s *AccountService) recordTest(ctx context.Context, id, model string, latencyMs int64, testErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load(ctx)
	if err != nil {
		return
	}
	idx := findAccount(list, id)
	if idx < 0 {
		return
	}
	list[idx].LastTestAt = time.Now().UTC().Format(time.RFC3339)
	list[idx].LastTestMs = latencyMs
	list[idx].LastTestModel = model
	if testErr != nil {
		list[idx].LastTestStatus = "fail"
		list[idx].LastTestError = truncStr(testErr.Error(), 300)
	} else {
		list[idx].LastTestStatus = "ok"
		list[idx].LastTestError = ""
	}
	_ = s.save(ctx, list)
}

// TestChat 单账号测试对话：用账号 OAuth 凭证调用 x.ai chat completions。
// onDelta 非空且 opts.Stream 为 true 时按增量回调（SSE 直播），否则整段返回。
// 结果（含失败原因）写回账号，列表可直接展示上次测试状态。
func (s *AccountService) TestChat(ctx context.Context, id string, opts TestChatOptions, onDelta func(string)) (TestChatResult, error) {
	res := TestChatResult{}
	acc, err := s.Get(ctx, id)
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(acc.AccessToken) == "" {
		return res, fmt.Errorf("账号无 OAuth 凭证（未入库转换），无法测试对话")
	}
	prompt := strings.TrimSpace(opts.Prompt)
	if prompt == "" {
		prompt = DefaultTestPrompt
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = DefaultTestModel
	}
	res.Model = model

	client, proxyUsed, err := newAccountHTTPClient(&acc, opts.UseProxy, 120*time.Second)
	if err != nil {
		s.recordTest(ctx, id, model, 0, err)
		return res, err
	}
	res.ProxyUsed = proxyUsed

	stream := opts.Stream && onDelta != nil
	payload := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   stream,
	}
	if stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	body, _ := json.Marshal(payload)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, accountBaseURL(&acc)+"/chat/completions", bytes.NewReader(body))
	if rerr != nil {
		s.recordTest(ctx, id, model, 0, rerr)
		return res, rerr
	}
	applyGrokCLIHeaders(req, acc.AccessToken)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("对话请求失败: %w", err)
		s.recordTest(ctx, id, model, time.Since(start).Milliseconds(), wrapped)
		return res, wrapped
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		wrapped := fmt.Errorf("上游 HTTP %d: %s", resp.StatusCode, truncStr(strings.TrimSpace(string(raw)), 300))
		s.recordTest(ctx, id, model, time.Since(start).Milliseconds(), wrapped)
		return res, wrapped
	}

	if stream {
		err = consumeChatStream(resp.Body, &res, onDelta)
	} else {
		err = consumeChatJSON(resp.Body, &res)
	}
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		s.recordTest(ctx, id, model, res.LatencyMs, err)
		return res, err
	}
	res.Reply = strings.TrimSpace(res.Reply)
	if res.Reply == "" {
		err = fmt.Errorf("上游未返回内容")
		s.recordTest(ctx, id, model, res.LatencyMs, err)
		return res, err
	}
	s.recordTest(ctx, id, model, res.LatencyMs, nil)
	return res, nil
}

// chatUsage OpenAI 兼容 usage 字段。
type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func applyUsage(res *TestChatResult, u *chatUsage) {
	if u == nil {
		return
	}
	if u.PromptTokens > 0 {
		res.PromptTokens = u.PromptTokens
	}
	if u.CompletionTokens > 0 {
		res.CompletionTokens = u.CompletionTokens
	}
	if u.TotalTokens > 0 {
		res.TotalTokens = u.TotalTokens
	}
}

// consumeChatJSON 解析非流式 chat completions 响应。
func consumeChatJSON(r io.Reader, res *TestChatResult) error {
	raw, _ := io.ReadAll(io.LimitReader(r, 1024*1024))
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *chatUsage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("解析上游响应失败: %v", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return fmt.Errorf("上游错误: %s", out.Error.Message)
	}
	applyUsage(res, out.Usage)
	if len(out.Choices) > 0 {
		res.Reply = out.Choices[0].Message.Content
	}
	return nil
}

// consumeChatStream 解析 SSE 流式 chat completions，按 delta 回调。
func consumeChatStream(r io.Reader, res *TestChatResult, onDelta func(string)) error {
	var sb strings.Builder
	scanner := bufio.NewScanner(io.LimitReader(r, 8*1024*1024))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *chatUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 忽略心跳/非 JSON 帧
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("上游错误: %s", chunk.Error.Message)
		}
		applyUsage(res, chunk.Usage)
		for _, ch := range chunk.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			sb.WriteString(ch.Delta.Content)
			onDelta(ch.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取流式响应失败: %v", err)
	}
	res.Reply = sb.String()
	return nil
}

// ModelInfo 单账号可用模型（上游 /models 返回，失败时调用方回落内置列表）。
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// ListModels 用账号凭证拉取上游 /models；顺带充当「凭证是否有效」的轻量校验。
func (s *AccountService) ListModels(ctx context.Context, id string, useProxy bool) ([]ModelInfo, error) {
	acc, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(acc.AccessToken) == "" {
		return nil, fmt.Errorf("账号无 OAuth 凭证（未入库转换）")
	}
	client, _, err := newAccountHTTPClient(&acc, useProxy, 30*time.Second)
	if err != nil {
		return nil, err
	}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, accountBaseURL(&acc)+"/models", nil)
	if rerr != nil {
		return nil, rerr
	}
	applyGrokCLIHeaders(req, acc.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取模型失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("上游 HTTP %d: %s", resp.StatusCode, truncStr(strings.TrimSpace(string(raw)), 200))
	}
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %v", err)
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		models = append(models, ModelInfo{ID: m.ID, DisplayName: m.DisplayName})
	}
	return models, nil
}

// VerifyCredential 凭证校验：拉一次 /models，成功即视为凭证可用（比对话省钱省时）。
// 结果同样写回测试留痕。
func (s *AccountService) VerifyCredential(ctx context.Context, id string, useProxy bool) (int, error) {
	start := time.Now()
	models, err := s.ListModels(ctx, id, useProxy)
	latency := time.Since(start).Milliseconds()
	s.recordTest(ctx, id, "credential", latency, err)
	if err != nil {
		return 0, err
	}
	return len(models), nil
}

// AccountPatch 单账号可编辑字段；nil 表示不改（区分「不传」与「置空」）。
type AccountPatch struct {
	Email    *string `json:"email"`
	Password *string `json:"password"`
	SSO      *string `json:"sso"`
	SSORW    *string `json:"sso_rw"`
	Proxy    *string `json:"proxy"`
	BaseURL  *string `json:"base_url"`
	Note     *string `json:"note"`
	Status   *string `json:"status"`
	Imported *bool   `json:"imported"`
	GroupID  *string `json:"group_id"`
	GroupName *string `json:"group_name"`
}

// Update 更新单账号字段（仅传入的字段生效）。
func (s *AccountService) Update(ctx context.Context, id string, patch AccountPatch) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load(ctx)
	if err != nil {
		return Account{}, err
	}
	idx := findAccount(list, id)
	if idx < 0 {
		return Account{}, fmt.Errorf("账号不存在")
	}
	acc := &list[idx]
	if patch.Email != nil {
		acc.Email = strings.TrimSpace(*patch.Email)
	}
	if patch.Password != nil {
		acc.Password = *patch.Password
	}
	if patch.SSO != nil {
		acc.SSO = strings.TrimSpace(*patch.SSO)
	}
	if patch.SSORW != nil {
		acc.SSORW = strings.TrimSpace(*patch.SSORW)
	}
	if patch.Proxy != nil {
		raw := strings.TrimSpace(*patch.Proxy)
		if raw != "" {
			trimmed, _, perr := proxyurl.Parse(raw)
			if perr != nil {
				return Account{}, fmt.Errorf("代理无效: %v", perr)
			}
			raw = trimmed
		}
		acc.Proxy = raw
	}
	if patch.BaseURL != nil {
		acc.BaseURL = strings.TrimSpace(*patch.BaseURL)
	}
	if patch.Note != nil {
		acc.Note = strings.TrimSpace(*patch.Note)
	}
	if patch.Status != nil {
		acc.Status = strings.TrimSpace(*patch.Status)
	}
	if patch.Imported != nil {
		acc.Imported = *patch.Imported
	}
	if patch.GroupID != nil {
		acc.GroupID = strings.TrimSpace(*patch.GroupID)
	}
	if patch.GroupName != nil {
		acc.GroupName = strings.TrimSpace(*patch.GroupName)
	}
	acc.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.save(ctx, list); err != nil {
		return Account{}, err
	}
	return *acc, nil
}

// MoveAccounts 批量移动账号到目标分组（groupID/groupName 均为空 = 清空分组）。
// 返回实际移动的账号数。不存在的 id 静默跳过。
func (s *AccountService) MoveAccounts(ctx context.Context, ids []string, groupID, groupName string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load(ctx)
	if err != nil {
		return 0, err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	moved := 0
	changed := false
	for i := range list {
		if !want[list[i].ID] {
			continue
		}
		groupID = strings.TrimSpace(groupID)
		groupName = strings.TrimSpace(groupName)
		if list[i].GroupID == groupID && list[i].GroupName == groupName {
			continue
		}
		list[i].GroupID = groupID
		list[i].GroupName = groupName
		list[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		moved++
		changed = true
	}
	if !changed {
		return 0, nil
	}
	if err := s.save(ctx, list); err != nil {
		return 0, err
	}
	return moved, nil
}

// RefreshOAuth 强制重新用 SSO 换一次 OAuth 凭证（已有 access_token 也覆盖）。
// 用于凭证过期/失效后的单账号自救，与批量「入库转换」的跳过逻辑区分。
func (s *AccountService) RefreshOAuth(ctx context.Context, id string, useProxy bool) (Account, error) {
	acc, err := s.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(acc.SSO) == "" {
		return Account{}, fmt.Errorf("账号无 SSO，无法重新换取凭证")
	}
	ssoRw := acc.SSORW
	if ssoRw == "" {
		ssoRw = acc.SSO
	}
	proxy := ""
	if useProxy {
		proxy = strings.TrimSpace(acc.Proxy)
	}
	// 与批量入库同一条重试/回退路径：代理坏了自动直连，限流自动退避
	tok, cerr := convertOnce(ctx, acc.SSO, ssoRw, proxy, proxy != "", grokregister.Logf)
	if cerr != nil {
		return Account{}, fmt.Errorf("换取凭证失败: %v", cerr)
	}
	if tok == nil {
		return Account{}, fmt.Errorf("换取凭证失败: 转换无返回")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load(ctx)
	if err != nil {
		return Account{}, err
	}
	idx := findAccount(list, id)
	if idx < 0 {
		return Account{}, fmt.Errorf("账号不存在")
	}
	target := &list[idx]
	target.AccessToken = tok.AccessToken
	target.RefreshToken = tok.RefreshToken
	target.IDToken = tok.IDToken
	target.TokenType = tok.TokenType
	target.ExpiresIn = tok.ExpiresIn
	target.Imported = true
	target.Status = "success"
	if strings.TrimSpace(target.BaseURL) == "" {
		target.BaseURL = xai.DefaultCLIBaseURL
	}
	target.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.save(ctx, list); err != nil {
		return Account{}, err
	}
	return *target, nil
}

// ConvertFailure 单条转换失败结果。
type ConvertFailure struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Error string `json:"error"`
}

// ConvertResult 批量入库转换结果。
type ConvertResult struct {
	Updated int              `json:"updated"`
	Failed  []ConvertFailure `json:"failed"`
	Skipped int              `json:"skipped"`
}

// convertOnce 执行一次 SSO→OAuth，带 sub2api 同款重试与代理回退：
//   - 代理侧失败 → 直连回退一次，不消耗重试次数
//   - 上游限流/抖动 → attempt²×2s 退避，最多 convertMaxAttempts 次
//   - 永久拒绝 → 立即返回，不再重试
//
// logf 可为 nil；返回最终 token 或最后一次错误。
func convertOnce(ctx context.Context, sso, ssoRw, proxy string, proxyOn bool, logf func(string, ...any)) (*grokregister.GrokOAuthToken, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// 每次转换都套 SSOConversionTimeout，避免设备流卡死拖垮整批
	ctx, cancel := context.WithTimeout(ctx, grokregister.SSOConversionTimeout)
	defer cancel()

	tryProxy := strings.TrimSpace(proxy)
	useProxy := proxyOn && tryProxy != "" && strings.Contains(tryProxy, "://")
	if !useProxy {
		tryProxy = ""
	}
	var lastErr error
	for attempt := 1; attempt <= convertMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		tok, err := grokregister.ConvertSSOToOAuth(ctx, sso, ssoRw, tryProxy, tryProxy != "")
		if err == nil && tok != nil {
			return tok, nil
		}
		if err == nil {
			err = fmt.Errorf("转换无返回")
		}
		lastErr = err
		msg := err.Error()

		// 代理这一跳不可用：直连再试一次，且不烧重试预算（与 sub2api 同策略）
		if tryProxy != "" && isProxyFailure(msg) {
			logf("[入库] 代理 Convert 失败，改直连重试: %v", err)
			tryProxy = ""
			attempt--
			continue
		}
		if isPermanentGrantDenial(msg) {
			return nil, err
		}
		if ctx.Err() != nil || !isTransientConvertError(err) || attempt >= convertMaxAttempts {
			return nil, err
		}
		backoff := convertBackoff(attempt)
		logf("[入库] 上游限流，%s 后重试 (%d/%d): %v", backoff, attempt, convertMaxAttempts, err)
		if serr := sleepCtx(ctx, backoff); serr != nil {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

// ConvertToOAuth 真入库：对选中账号执行 SSO→OAuth 转换（与 sub2api 一致）。
// 成功 → 写入 OAuth 凭证并标记已入库；失败 → 保留原账号并返回原因（可重试）。
//
// proxyMode 为空时读取「代理设置 → 导入出口」（ProxySettings.ImportMode），
// 与代理池页面的配置一致；显式传入则以传入为准。
func (s *AccountService) ConvertToOAuth(ctx context.Context, ids []string, proxyMode string) (ConvertResult, error) {
	res := ConvertResult{Failed: []ConvertFailure{}}
	if len(ids) == 0 {
		return res, nil
	}
	// 解析导入出口：尊重用户在代理池页面配置的 import_mode / import_urls。
	// 之前这里只看账号自带的注册代理，用户配「直连」也照样走代理 → 代理过期即全失败。
	ps, psErr := s.GetProxySettings(ctx)
	if psErr != nil {
		ps = ProxySettings{ImportMode: "direct"}
	}
	mode := strings.TrimSpace(proxyMode)
	if mode == "" {
		mode = ps.ImportMode
	}
	mode = normalizeProxyMode(mode, "direct")

	// 阶段一：持锁只做快照与分流，立刻释放。
	// 绝不能把 s.mu 带进网络调用——SSO→OAuth 可能跑几分钟，期间账号列表
	// （List/Get，即前端刷新）会全部阻塞；而且原实现在成功路径上根本没有解锁，
	// 一次入库之后整个账号服务永久死锁，表现就是「入库总是失败」。
	type convertJob struct {
		ID    string
		Email string
		SSO   string
		SSORW string
		Proxy string
	}
	var jobs []convertJob
	markImported := make([]string, 0, len(ids))

	s.mu.Lock()
	list, err := s.load(ctx)
	if err != nil {
		s.mu.Unlock()
		return res, err
	}
	byID := map[string]int{}
	for i := range list {
		byID[list[i].ID] = i
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		// 去重：同一 ID 重复传入会让两个 goroutine 并发写同一条账号
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		idx, ok := byID[id]
		if !ok {
			res.Skipped++
			continue
		}
		acc := &list[idx]
		if strings.TrimSpace(acc.AccessToken) != "" {
			// 已有凭证：无需触网，直接标记已入库
			markImported = append(markImported, acc.ID)
			continue
		}
		if strings.TrimSpace(acc.SSO) == "" {
			res.Skipped++
			continue
		}
		ssoRw := acc.SSORW
		if ssoRw == "" {
			ssoRw = acc.SSO
		}
		jobs = append(jobs, convertJob{ID: acc.ID, Email: acc.Email, SSO: acc.SSO, SSORW: ssoRw, Proxy: acc.Proxy})
	}
	// 已有凭证的先落盘，免得后面转换耗时长、这部分结果迟迟不可见
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range markImported {
		if i, ok := byID[id]; ok {
			list[i].Imported = true
			list[i].UpdatedAt = now
			res.Updated++
			// 这条快路径也要计入进度：它不进阶段二的转换循环，
			// 漏报会让进度停在 0/N 而结果却是全部成功（实测 6 个 13ms 完成，
			// 面板显示 0/6）。
			if s.importProg != nil {
				s.importProg.finishOne(list[i].Email+"（已有凭证，免转换）", true, "")
			}
		}
	}
	if s.importProg != nil {
		// 无 SSO / 找不到账号的跳过项也要反映出来，否则 done 永远到不了 total
		s.importProg.addSkipped(0)
	}
	if len(markImported) > 0 {
		if serr := s.save(ctx, list); serr != nil {
			s.mu.Unlock()
			return res, serr
		}
	}
	s.mu.Unlock()

	if len(jobs) == 0 {
		return res, nil
	}

	// 阶段二：不持锁做网络转换（小并发 4，避免打爆上游）
	type convertOutcome struct {
		job convertJob
		tok *grokregister.GrokOAuthToken
		err error
	}
	outcomes := make([]convertOutcome, len(jobs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	// 进度上报：这段是耗时主体（每个账号最多 convertMaxAttempts 次退避重试），
	// 不逐个上报的话前端只能看着按钮转圈，分不清是在重试还是卡死。
	prog := s.importProg
	if prog != nil {
		prog.stage("converting", fmt.Sprintf("开始转换 %d 个账号（并发 4）…", len(jobs)))
	}
	for i, job := range jobs {
		wg.Add(1)
		go func(slot int, j convertJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if prog != nil {
				prog.startOne(j.Email)
			}
			// 按导入出口挑代理：direct→空，registration→账号注册代理，
			// global/selected→导入池轮询（PickImportProxy 之前从未被调用）
			proxy := PickImportProxy(mode, ps.ImportURLs, ps.RegisterURLs, j.Proxy, slot)
			tok, cerr := convertOnce(ctx, j.SSO, j.SSORW, proxy, proxy != "", grokregister.Logf)
			outcomes[slot] = convertOutcome{job: j, tok: tok, err: cerr}
			if prog != nil {
				if cerr == nil && tok != nil {
					prog.finishOne(j.Email, true, "")
				} else {
					msg := "转换无返回"
					if cerr != nil {
						msg = cerr.Error()
					}
					prog.finishOne(j.Email, false, msg)
				}
			}
		}(i, job)
	}
	wg.Wait()
	if prog != nil {
		prog.stage("saving", "转换完成，正在写入账号库…")
	}

	// 阶段三：重新持锁合并结果（按 ID 重新定位，期间列表可能已变动）
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err = s.load(ctx)
	if err != nil {
		return res, err
	}
	byID = map[string]int{}
	for i := range list {
		byID[list[i].ID] = i
	}
	now = time.Now().UTC().Format(time.RFC3339)
	for _, out := range outcomes {
		if out.err == nil && out.tok != nil {
			idx, ok := byID[out.job.ID]
			if !ok {
				// 转换期间账号被删了：不复活它
				res.Skipped++
				continue
			}
			a := &list[idx]
			a.AccessToken = out.tok.AccessToken
			a.RefreshToken = out.tok.RefreshToken
			a.IDToken = out.tok.IDToken
			a.TokenType = out.tok.TokenType
			a.ExpiresIn = out.tok.ExpiresIn
			a.Imported = true
			a.Status = "success"
			// 入库后钉住 CLI 网关（与 sub2api 入库写 base_url 一致）
			if strings.TrimSpace(a.BaseURL) == "" {
				a.BaseURL = xai.DefaultCLIBaseURL
			}
			a.UpdatedAt = now
			res.Updated++
			continue
		}
		fail := out.err
		if fail == nil {
			fail = fmt.Errorf("转换无返回")
		}
		res.Failed = append(res.Failed, ConvertFailure{ID: out.job.ID, Email: out.job.Email, Error: fail.Error()})
		// 标记「待检查」：注册尾部的 device OAuth 验活已关掉（省 13.5s/账号，
		// 因为这里的转换本来就会走一次 OAuth）。代价是坏账号不再当场暴露，
		// 所以转换失败必须在账号列表上留下痕迹，而不是只写进日志。
		if idx, ok := byID[out.job.ID]; ok {
			a := &list[idx]
			a.LastTestStatus = "fail"
			a.LastTestError = "入库转换失败（待检查）: " + truncStr(fail.Error(), 160)
			a.LastTestAt = now
			a.UpdatedAt = now
		}
	}
	if err := s.save(ctx, list); err != nil {
		return res, err
	}
	return res, nil
}
