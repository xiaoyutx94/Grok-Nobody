package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/umbraforge/desktop/internal/store"
)

// mockICloudPlatform 模拟 iCloud Privacy Mail 平台：
// 登录（cookie 会话）、账号、邮箱、创建、claim、取码。
type mockICloudPlatform struct {
	accounts  []IcloudAccount
	mailboxes []IcloudMailbox
	username  string
	password  string
	session   string
	apiKey    string
}

func newMockPlatform(t *testing.T) (*mockICloudPlatform, *httptest.Server) {
	m := &mockICloudPlatform{
		username: "admin", password: "secret", apiKey: "global-key",
		accounts: []IcloudAccount{
			{ID: "acc-1", Label: "主力", AppleID: "a@icloud.com"},
			{ID: "acc-2", Label: "备用", AppleID: "b@icloud.com"},
		},
		mailboxes: []IcloudMailbox{
			{ID: "mb-1", Email: "alias1@icloud.com", AccountID: "acc-1", Status: "available", APIURL: "http://p/api/v1/mailboxes/alias1%40icloud.com/code?key=k1"},
			{ID: "mb-2", Email: "alias2@icloud.com", AccountID: "acc-2", Status: "available", APIURL: "http://p/api/v1/mailboxes/alias2%40icloud.com/code?key=k2"},
		},
		session: "sess-token",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/login":
			var p struct{ Username, Password string }
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Username == "" || p.Password == "" {
				writeMockErr(w, 401, "bad_credentials", "账号或密码错误")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: m.session})
			writeMockJSON(w, map[string]any{"success": true, "user": map[string]any{"username": p.Username}})
		case r.URL.Path == "/api/auth/register":
			var p struct{ Username, Password string }
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Username == "" || p.Password == "" {
				writeMockErr(w, 400, "bad_request", "缺少账号或密码")
				return
			}
			writeMockJSON(w, map[string]any{"success": true, "user": map[string]any{"username": p.Username}})
		case r.URL.Path == "/api/accounts":
			if !mockAuthed(r, m) {
				writeMockErr(w, 401, "unauthorized", "需要登录")
				return
			}
			writeMockJSON(w, map[string]any{"success": true, "accounts": m.accounts})
		case r.URL.Path == "/api/mailboxes" && r.Method == http.MethodGet:
			if !mockAuthed(r, m) {
				writeMockErr(w, 401, "unauthorized", "需要登录")
				return
			}
			writeMockJSON(w, map[string]any{"success": true, "mailboxes": m.mailboxes})
		case r.URL.Path == "/api/icloud/mailboxes/create":
			if !mockAuthed(r, m) {
				writeMockErr(w, 401, "unauthorized", "需要登录")
				return
			}
			var p struct {
				AccountID string `json:"account_id"`
				Count     int    `json:"count"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			n := p.Count
			if n < 1 {
				n = 1
			}
			for i := 0; i < n; i++ {
				aid := p.AccountID
				if aid == "" {
					aid = m.accounts[0].ID
				}
				m.mailboxes = append(m.mailboxes, IcloudMailbox{
					ID: fmt.Sprintf("mb-new-%d", len(m.mailboxes)+1),
					Email: fmt.Sprintf("alias-new-%d@icloud.com", len(m.mailboxes)+1),
					AccountID: aid, Status: "available",
					APIURL: fmt.Sprintf("http://p/api/v1/mailboxes/alias-new-%d%%40icloud.com/code?key=k%d", len(m.mailboxes)+1, len(m.mailboxes)+1),
				})
			}
			writeMockJSON(w, map[string]any{"created": n, "failures": []any{}})
		case r.URL.Path == "/api/v1/mailboxes/claim":
			if r.Header.Get("Authorization") != "Bearer "+m.apiKey {
				writeMockErr(w, 401, "invalid_api_key", "API Key 错误")
				return
			}
			writeMockJSON(w, map[string]any{"success": true, "mailbox": m.mailboxes[0]})
		default:
			// 取码：/api/v1/mailboxes/{email}/code
			if strings.HasPrefix(r.URL.Path, "/api/v1/mailboxes/") && strings.HasSuffix(r.URL.Path, "/code") {
				if !strings.Contains(r.URL.RawQuery, "key=") {
					writeMockErr(w, 401, "invalid_api_key", "缺 key")
					return
				}
				if r.URL.Query().Get("after") == "" {
					writeMockJSON(w, map[string]any{"success": false, "error": map[string]any{"code": "no_code", "message": "暂未收到验证码"}})
					return
				}
				writeMockJSON(w, map[string]any{"success": true, "email": "alias1@icloud.com", "code": "135790"})
				return
			}
			writeMockErr(w, 404, "not_found", "未找到")
		}
	}))
	// APIURL 占位前缀换成真实 srv 地址（测试请求要能打回来）
	for i := range m.mailboxes {
		m.mailboxes[i].APIURL = srv.URL + strings.TrimPrefix(m.mailboxes[i].APIURL, "http://p")
	}
	return m, srv
}

func mockAuthed(r *http.Request, m *mockICloudPlatform) bool {
	c, err := r.Cookie("session")
	return err == nil && c.Value == m.session
}

func writeMockJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func writeMockErr(w http.ResponseWriter, code int, ec, msg string) {
	w.WriteHeader(code)
	writeMockJSON(w, map[string]any{"success": false, "error": map[string]any{"code": ec, "message": msg}})
}

// TestIcloudGatewayLoginAndAccounts 登录（cookie 会话）+ 账号列表。
func TestIcloudGatewayLoginAndAccounts(t *testing.T) {
	_, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)
	ctx := context.Background()

	if err := g.Login(ctx, srv.URL, "admin", ""); err == nil {
		t.Fatal("空密码应失败")
	}
	if err := g.Login(ctx, srv.URL, "admin", "secret"); err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if !g.LoggedIn(srv.URL) {
		t.Fatal("应处于登录态")
	}
	accs, err := g.Accounts(ctx, srv.URL)
	if err != nil {
		t.Fatalf("账号列表失败: %v", err)
	}
	if len(accs) != 2 || accs[0].ID != "acc-1" {
		t.Fatalf("账号列表错误: %+v", accs)
	}
}

// TestIcloudSyncPoolAndClaim 同步平台邮箱到本地池 + 按账号过滤领取 + 释放。
func TestIcloudSyncPoolAndClaim(t *testing.T) {
	m, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)
	ctx := context.Background()
	_ = g.Login(ctx, srv.URL, "admin", "secret")

	added, err := g.SyncPool(ctx, srv.URL, m.accounts)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if added != 2 {
		t.Fatalf("新增应 2 条，got %d", added)
	}
	pool := g.Pool(nil)
	if len(pool.Entries) != 2 {
		t.Fatalf("池应有 2 条，got %d", len(pool.Entries))
	}

	// 按账号过滤领取：只领 acc-2
	ent := g.ClaimFromPool([]string{"acc-2"})
	if ent == nil || ent.AccountID != "acc-2" {
		t.Fatalf("应按账号过滤领到 acc-2，got %+v", ent)
	}
	if ent.Status != "used" {
		t.Fatalf("领取后应为 used，got %s", ent.Status)
	}
	// 池里 acc-1 的还在
	ent2 := g.ClaimFromPool([]string{"acc-1"})
	if ent2 == nil || ent2.Email != "alias1@icloud.com" {
		t.Fatalf("应领到 acc-1 的邮箱，got %+v", ent2)
	}
	// acc-2 已领完 → 池空
	if ent3 := g.ClaimFromPool([]string{"acc-2"}); ent3 != nil {
		t.Fatalf("acc-2 池应空，got %+v", ent3)
	}
	// 释放后能再领
	g.ReleasePoolEmail("alias2@icloud.com")
	ent4 := g.ClaimFromPool([]string{"acc-2"})
	if ent4 == nil || ent4.Email != "alias2@icloud.com" {
		t.Fatalf("释放后应能再领 alias2，got %+v", ent4)
	}
	// 再同步不重复
	added2, _ := g.SyncPool(ctx, srv.URL, m.accounts)
	if added2 != 0 {
		t.Fatalf("重复同步应 0 新增，got %d", added2)
	}
}

// TestIcloudPoolEmailCreateWait 池模式 EmailService 全链路：
// 领取（账号过滤）→ 取码 → 释放。
func TestIcloudPoolEmailCreateWait(t *testing.T) {
	m, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)
	ctx := context.Background()
	_ = g.Login(ctx, srv.URL, "admin", "secret")
	_, _ = g.SyncPool(ctx, srv.URL, m.accounts)

	svc := NewICloudPoolEmail(g, srv.URL, "global-key", "openai", []string{"acc-1"})
	addr, err := svc.Create()
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if addr != "alias1@icloud.com" {
		t.Fatalf("应领到 acc-1 邮箱，got %s", addr)
	}
	code, err := svc.WaitForCode(5*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("取码失败: %v", err)
	}
	if code != "135790" {
		t.Fatalf("验证码错误: %s", code)
	}
	if err := svc.ResetForRetry(); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	// 释放后可再领（同一邮箱回池）
	addr2, _ := svc.Create()
	if addr2 != "alias1@icloud.com" {
		t.Fatalf("释放后应能再领，got %s", addr2)
	}
}

// TestIcloudPoolEmailFallsBackToClaim 池空时回退平台 claim。
func TestIcloudPoolEmailFallsBackToClaim(t *testing.T) {
	_, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)

	svc := NewICloudPoolEmail(g, srv.URL, "global-key", "openai", nil)
	addr, err := svc.Create()
	if err != nil {
		t.Fatalf("claim 回退失败: %v", err)
	}
	if !strings.Contains(addr, "icloud.com") {
		t.Fatalf("claim 应返回邮箱，got %s", addr)
	}
}

// TestIcloudPoolEmailAutoCreate 池空时用登录账号自动创建随机子邮箱再领取
// （用户期望：登录 Apple 后注册无需手动添加子邮箱）。
func TestIcloudPoolEmailAutoCreate(t *testing.T) {
	m, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)
	ctx := context.Background()
	// 登录（自动账号），但不同步池 → 池空
	if err := g.ensurePanelAccount(ctx, srv.URL, "umbraforge-test", "uf-t-1"); err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	// 清空平台预置邮箱：模拟「登录 Apple 后一个子邮箱都没有」的真实场景
	m.mailboxes = nil
	if p := g.Pool(nil); len(p.Entries) != 0 {
		t.Fatalf("前置条件：池应为空，got %d", len(p.Entries))
	}

	svc := NewICloudPoolEmail(g, srv.URL, "global-key", "openai", []string{"acc-1"})
	addr, err := svc.Create()
	if err != nil {
		t.Fatalf("自动创建失败: %v", err)
	}
	if !strings.Contains(addr, "icloud.com") || !strings.HasPrefix(addr, "alias-new") {
		t.Fatalf("应领到自动创建的新邮箱，got %s", addr)
	}
	// 自动创建的邮箱已进本地池并标记 used
	p := g.Pool(nil)
	if len(p.Entries) < 1 {
		t.Fatalf("池应有新邮箱，got %d", len(p.Entries))
	}
	found := false
	for _, e := range p.Entries {
		if e.Email == addr && e.Status == "used" {
			found = true
		}
	}
	if !found {
		t.Fatalf("领到的邮箱应在池中且 used，got %+v", p.Entries)
	}
}

// TestIcloudEnsurePanelAccount 内置服务自动账号：注册（不存在）→ 登录 → 可用。
func TestIcloudEnsurePanelAccount(t *testing.T) {
	_, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)
	ctx := context.Background()

	if err := g.ensurePanelAccount(ctx, srv.URL, "umbraforge-1", "uf-1-1"); err != nil {
		t.Fatalf("首次注册+登录失败: %v", err)
	}
	// 再跑一次（已存在）应幂等成功
	if err := g.ensurePanelAccount(ctx, srv.URL, "umbraforge-1", "uf-1-1"); err != nil {
		t.Fatalf("重复初始化应幂等成功: %v", err)
	}
	accs, err := g.Accounts(ctx, srv.URL)
	if err != nil || len(accs) != 2 {
		t.Fatalf("会话应可访问账号列表: %v %d", err, len(accs))
	}
}

// TestIcloudCreateMailboxes 平台创建随机子邮箱（登录态）。
func TestIcloudCreateMailboxes(t *testing.T) {
	_, srv := newMockPlatform(t)
	defer srv.Close()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewIcloudGateway(st)
	ctx := context.Background()
	_ = g.Login(ctx, srv.URL, "admin", "secret")

	n, err := g.CreateMailboxes(ctx, srv.URL, "acc-1", 2)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应创建 2 个，got %d", n)
	}
}
