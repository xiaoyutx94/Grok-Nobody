package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/umbraforge/desktop/internal/engine"
	"github.com/umbraforge/desktop/internal/store"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *store.JSONStore {
	t.Helper()
	st, err := store.NewJSONStore(t.TempDir())
	require.NoError(t, err)
	return st
}

func seedAccounts(t *testing.T, st *store.JSONStore, list []engine.Account) {
	t.Helper()
	svc := engine.NewAccountService(st)
	ctx := context.Background()
	for i := range list {
		_, err := svc.UpsertFromResult(ctx, map[string]any{
			"email":        list[i].Email,
			"access_token": list[i].AccessToken,
			"group_id":     list[i].GroupID,
			"group_name":   list[i].GroupName,
		})
		require.NoError(t, err)
	}
}

func TestPickerPickFiltersByGroupAndToken(t *testing.T) {
	st := newTestStore(t)
	seedAccounts(t, st, []engine.Account{
		{Email: "a@x.com", AccessToken: "tok-a", GroupID: "g1", GroupName: "组1"},
		{Email: "b@x.com", AccessToken: "tok-b", GroupID: "g1", GroupName: "组1"},
		{Email: "c@x.com", AccessToken: "", GroupID: "g1", GroupName: "组1"},   // 无 token 不可用
		{Email: "d@x.com", AccessToken: "tok-d", GroupID: "g2", GroupName: "组2"}, // 其他分组
	})
	p := NewAccountPicker(engine.NewAccountService(st))
	ctx := context.Background()

	got := map[string]bool{}
	for i := 0; i < 6; i++ {
		entry, err := p.Pick(ctx, "g1", nil)
		require.NoError(t, err)
		require.Equal(t, "g1", entry.Account.GroupID)
		require.NotEmpty(t, entry.Account.AccessToken)
		got[entry.Account.Email] = true
		p.Release(entry.Account.ID)
	}
	require.Equal(t, map[string]bool{"a@x.com": true, "b@x.com": true}, got)
}

func TestPickerConcurrencyLimit(t *testing.T) {
	st := newTestStore(t)
	seedAccounts(t, st, []engine.Account{
		{Email: "a@x.com", AccessToken: "tok-a", GroupID: "g1", GroupName: "组1"},
	})
	p := NewAccountPicker(engine.NewAccountService(st))
	ctx := context.Background()

	// 占满单账号并发上限
	var entries []AccountEntry
	for i := 0; i < MaxAccountConcurrency; i++ {
		e, err := p.Pick(ctx, "g1", nil)
		require.NoError(t, err)
		entries = append(entries, e)
	}
	// 超并发 → 无可用账号
	_, err := p.Pick(ctx, "g1", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "无可用账号")

	// 释放一个后恢复
	p.Release(entries[0].Account.ID)
	e, err := p.Pick(ctx, "g1", nil)
	require.NoError(t, err)
	require.Equal(t, "a@x.com", e.Account.Email)
	p.Release(e.Account.ID)
	for _, x := range entries[1:] {
		p.Release(x.Account.ID)
	}
}

func TestPickerCooldownAndForbidden(t *testing.T) {
	st := newTestStore(t)
	seedAccounts(t, st, []engine.Account{
		{Email: "a@x.com", AccessToken: "tok-a", GroupID: "g1", GroupName: "组1"},
		{Email: "b@x.com", AccessToken: "tok-b", GroupID: "g1", GroupName: "组1"},
	})
	p := NewAccountPicker(engine.NewAccountService(st))
	ctx := context.Background()

	// 拿真实账号 ID
	e1, err := p.Pick(ctx, "g1", nil)
	require.NoError(t, err)
	e2, err := p.Pick(ctx, "g1", nil)
	require.NoError(t, err)
	require.NotEqual(t, e1.Account.ID, e2.Account.ID)
	p.Release(e1.Account.ID)
	p.Release(e2.Account.ID)

	// 429 → 冷却 30min，立即不可用
	p.RecordFailure(e1.Account.ID, 429, "")
	got, err := p.Pick(ctx, "g1", nil)
	require.NoError(t, err) // e2 仍可用
	require.Equal(t, e2.Account.ID, got.Account.ID)
	p.Release(got.Account.ID)

	// 403 → 永久封禁；清空冷却后依然无可用（e1 永久封禁 + e2 冷却清空后仍可用？不对——只封 e2）
	p.RecordFailure(e2.Account.ID, 403, "forbidden")
	p.mu.Lock()
	p.cooldown = map[string]time.Time{}
	p.mu.Unlock()
	// e1 冷却已清 → e1 可用
	got, err = p.Pick(ctx, "g1", nil)
	require.NoError(t, err)
	require.Equal(t, e1.Account.ID, got.Account.ID)
	p.Release(got.Account.ID)

	// e2 永久封禁 → 再取不会拿到 e2
	p.mu.Lock()
	p.cooldown = map[string]time.Time{}
	p.mu.Unlock()
	got, err = p.Pick(ctx, "g1", nil)
	require.NoError(t, err)
	require.Equal(t, e1.Account.ID, got.Account.ID, "封禁账号不应再被选中")
	p.Release(got.Account.ID)
}

func TestPickerProxyRotation(t *testing.T) {
	st := newTestStore(t)
	seedAccounts(t, st, []engine.Account{
		{Email: "a@x.com", AccessToken: "tok-a", GroupID: "g1", GroupName: "组1"},
	})
	p := NewAccountPicker(engine.NewAccountService(st))
	ctx := context.Background()

	proxies := []string{"http://p1", "http://p2", "http://p3"}
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		e, err := p.Pick(ctx, "g1", proxies)
		require.NoError(t, err)
		require.NotEmpty(t, e.Proxy)
		seen[e.Proxy] = true
		p.Release(e.Account.ID)
	}
	require.Len(t, seen, 3, "多轮请求应轮询到全部代理")

	// 无分组代理 → 直连
	e, err := p.Pick(ctx, "g1", nil)
	require.NoError(t, err)
	require.Equal(t, "", e.Proxy)
	p.Release(e.Account.ID)
}

func TestPickerSnapshot(t *testing.T) {
	st := newTestStore(t)
	seedAccounts(t, st, []engine.Account{
		{Email: "a@x.com", AccessToken: "tok-a", GroupID: "g1", GroupName: "组1"},
		{Email: "b@x.com", AccessToken: "tok-b", GroupID: "g1", GroupName: "组1"},
		{Email: "c@x.com", AccessToken: "tok-c", GroupID: "g1", GroupName: "组1"},
		{Email: "d@x.com", AccessToken: "tok-d", GroupID: "g2", GroupName: "组2"},
	})
	p := NewAccountPicker(engine.NewAccountService(st))
	ctx := context.Background()

	// 拿真实账号 ID 标记故障
	var ids []string
	for i := 0; i < 3; i++ {
		e, err := p.Pick(ctx, "g1", nil)
		require.NoError(t, err)
		ids = append(ids, e.Account.ID)
		p.Release(e.Account.ID)
	}
	p.RecordFailure(ids[0], 429, "")
	p.RecordFailure(ids[1], 403, "forbidden")

	snap, err := p.Snapshot(ctx, "g1")
	require.NoError(t, err)
	require.Equal(t, 3, snap.Total)
	require.Equal(t, 1, snap.Available)
	require.Equal(t, 1, snap.Cooldown)
	require.Equal(t, 1, snap.Forbidden)
}

func TestGatewayStoreDefaults(t *testing.T) {
	st := newTestStore(t)
	gs := NewGatewayStore(st)
	ctx := context.Background()
	require.NoError(t, gs.EnsureDefaults(ctx))

	svcs, err := gs.ListServices(ctx)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, "grok", svcs[0].Type)
	require.True(t, strings.Contains(svcs[0].BaseURL, "cli-chat-proxy.grok.com"))

	groups, err := gs.ListGroups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, DefaultServiceID, groups[0].ServiceID)

	cfg, err := gs.GetConfig(ctx)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, DefaultPort, cfg.Port)

	// 幂等：二次初始化不重复
	require.NoError(t, gs.EnsureDefaults(ctx))
	svcs2, _ := gs.ListServices(ctx)
	require.Len(t, svcs2, 1)
}

func TestGatewayStoreKeyLifecycle(t *testing.T) {
	st := newTestStore(t)
	gs := NewGatewayStore(st)
	ctx := context.Background()
	require.NoError(t, gs.EnsureDefaults(ctx))

	k, err := gs.CreateKey(ctx, "测试", DefaultGroupID)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(k.KeyFull, "sk-"))
	require.True(t, strings.Contains(k.Key, "…"), "列表视图应脱敏")

	// 认证查询（完整密钥）
	found, err := gs.GetKeyBySecret(ctx, k.KeyFull)
	require.NoError(t, err)
	require.Equal(t, DefaultGroupID, found.GroupID)

	// 禁用后认证失败
	_, err = gs.SetKeyStatus(ctx, k.ID, KeyStatusDisabled)
	require.NoError(t, err)
	_, err = gs.GetKeyBySecret(ctx, k.KeyFull)
	require.Error(t, err)

	// 删除
	require.NoError(t, gs.DeleteKey(ctx, k.ID))
	_, err = gs.GetKeyBySecret(ctx, k.KeyFull)
	require.Error(t, err)
}

func TestGatewayStoreGroupProxyBinding(t *testing.T) {
	st := newTestStore(t)
	gs := NewGatewayStore(st)
	ctx := context.Background()
	require.NoError(t, gs.EnsureDefaults(ctx))

	g, err := gs.CreateGroup(ctx, "代理组", "", DefaultServiceID, []string{"http://p1:8080", "http://p1:8080", "  "})
	require.NoError(t, err)
	require.Equal(t, []string{"http://p1:8080"}, g.ProxyIDs, "重复/空白代理应去重")

	// 更新代理绑定
	_, err = gs.UpdateGroup(ctx, g.ID, map[string]any{"proxy_ids": []any{"http://p2", "http://p3"}})
	require.NoError(t, err)
	got, err := gs.GetGroup(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"http://p2", "http://p3"}, got.ProxyIDs)

	// 默认分组不可删
	require.Error(t, gs.DeleteGroup(ctx, DefaultGroupID))
	// 内置服务不可删
	require.Error(t, gs.DeleteService(ctx, DefaultServiceID))
}

func TestGatewayGetKeyFull(t *testing.T) {
	st := newTestStore(t)
	gs := NewGatewayStore(st)
	ctx := context.Background()
	require.NoError(t, gs.EnsureDefaults(ctx))

	k, err := gs.CreateKey(ctx, "复制测试", DefaultGroupID)
	require.NoError(t, err)

	full, err := gs.GetKeyFull(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, k.KeyFull, full.KeyFull, "reveal 必须返回完整密钥")
	require.Contains(t, full.Key, "…", "响应展示字段仍应脱敏")

	_, err = gs.GetKeyFull(ctx, "不存在")
	require.Error(t, err)
}

func TestGatewayConfigListenHost(t *testing.T) {
	st := newTestStore(t)
	gs := NewGatewayStore(st)
	ctx := context.Background()
	require.NoError(t, gs.EnsureDefaults(ctx))

	// 默认 0.0.0.0（内外网）
	cfg, err := gs.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultListenHost, cfg.ListenHost)

	// 非法 host 拒绝
	require.Error(t, gs.SetConfig(ctx, GatewayConfig{Enabled: true, Port: 18789, ListenHost: "8.8.8.8"}))
	// 合法切换
	require.NoError(t, gs.SetConfig(ctx, GatewayConfig{Enabled: true, Port: 18789, ListenHost: "127.0.0.1"}))
	cfg, _ = gs.GetConfig(ctx)
	require.Equal(t, "127.0.0.1", cfg.ListenHost)
	// 空 host 回落默认
	require.NoError(t, gs.SetConfig(ctx, GatewayConfig{Enabled: true, Port: 18789}))
	cfg, _ = gs.GetConfig(ctx)
	require.Equal(t, DefaultListenHost, cfg.ListenHost)
}
