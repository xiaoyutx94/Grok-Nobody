package engine

import (
	"context"
	"testing"
)

// TestSyncWarpProxyEntriesJoinsPool WARP 实例必须作为代理池条目出现 ——
// 这是用户报的「代理池怎么没有同步 warp 的代理」。
//
// 旧实现只往 ProxySettings.RegisterURLs 追加 URL，而代理池页渲染的是 Entries，
// 两套数据，所以 WARP 从来不显示；且代理池页的出口设置自动保存会把
// RegisterURLs 抹成空（cleanURLs(nil) → []），WARP 连后台生效都做不到。
func TestSyncWarpProxyEntriesJoinsPool(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)

	// 用户已有 2 条手动代理
	manual := []ProxyEntry{
		{URL: "socks5://1.1.1.1:1080", Note: "香港Z09 | IEPL", Enabled: true},
		{URL: "socks5://2.2.2.2:1080", Note: "日本Z05", Enabled: false},
	}
	if _, err := s.SaveProxyPool(ctx, ProxyPool{Entries: manual}); err != nil {
		t.Fatalf("SaveProxyPool 失败: %v", err)
	}

	insts := []WarpInstance{
		{Name: "umbra-warp-41000", Container: "umbra-warp-41000", ProxyURL: "socks5://127.0.0.1:41000", Status: "running"},
		{Name: "umbra-warp-41001", Container: "umbra-warp-41001", ProxyURL: "socks5://127.0.0.1:41001", Status: "running"},
	}
	pool, err := s.SyncWarpProxyEntries(ctx, insts)
	if err != nil {
		t.Fatalf("SyncWarpProxyEntries 失败: %v", err)
	}

	if len(pool.Entries) != 4 {
		t.Fatalf("Entries = %d, want 4（2 手动 + 2 WARP）", len(pool.Entries))
	}
	// 手动条目必须原样保留，包括被禁用的那条
	byURL := map[string]ProxyEntry{}
	for _, e := range pool.Entries {
		byURL[e.URL] = e
	}
	if e := byURL["socks5://2.2.2.2:1080"]; e.Enabled || e.Note != "日本Z05" || e.Source != "" {
		t.Errorf("手动条目被篡改: %+v", e)
	}
	// WARP 条目必须带 source 标记且默认启用
	for _, u := range []string{"socks5://127.0.0.1:41000", "socks5://127.0.0.1:41001"} {
		e, ok := byURL[u]
		if !ok {
			t.Errorf("WARP 出口 %s 没进代理池", u)
			continue
		}
		if e.Source != ProxySourceWarp {
			t.Errorf("%s Source = %q, want %q", u, e.Source, ProxySourceWarp)
		}
		if !e.Enabled {
			t.Errorf("%s 应默认启用（用户部署了就是要用）", u)
		}
	}
	// 注册消费 pool.URLs：必须包含 2 个 WARP + 1 个启用的手动代理
	if len(pool.URLs) != 3 {
		t.Errorf("URLs = %d (%v), want 3（禁用的手动代理不算）", len(pool.URLs), pool.URLs)
	}
}

// TestSyncWarpProxyEntriesRemovesDeletedContainer 容器删除后条目必须消失，
// 否则会作为「僵尸出口」继续参与注册，每次取到都连不上。
func TestSyncWarpProxyEntriesRemovesDeletedContainer(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)

	two := []WarpInstance{
		{Name: "w0", ProxyURL: "socks5://127.0.0.1:41000", Status: "running"},
		{Name: "w1", ProxyURL: "socks5://127.0.0.1:41001", Status: "running"},
	}
	if _, err := s.SyncWarpProxyEntries(ctx, two); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	// 删掉一个容器
	one := two[:1]
	pool, err := s.SyncWarpProxyEntries(ctx, one)
	if err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}
	if len(pool.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1（被删的容器要淘汰）", len(pool.Entries))
	}
	if pool.Entries[0].URL != "socks5://127.0.0.1:41000" {
		t.Errorf("留下的是 %q, want socks5://127.0.0.1:41000", pool.Entries[0].URL)
	}
}

// TestSyncWarpProxyEntriesKeepsUserDisableChoice 用户手动停用某个 WARP 出口后，
// 再次同步不能把它改回启用 —— 否则「停用」这个操作等于无效。
func TestSyncWarpProxyEntriesKeepsUserDisableChoice(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)

	insts := []WarpInstance{{Name: "w0", ProxyURL: "socks5://127.0.0.1:41000", Status: "running"}}
	if _, err := s.SyncWarpProxyEntries(ctx, insts); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	// 用户在代理池页把它停用
	pool, _ := s.GetProxyPool(ctx)
	pool.Entries[0].Enabled = false
	if _, err := s.SaveProxyPool(ctx, pool); err != nil {
		t.Fatalf("保存停用状态失败: %v", err)
	}
	// 再同步一次（例如新建了别的实例触发）
	again, err := s.SyncWarpProxyEntries(ctx, insts)
	if err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}
	if len(again.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(again.Entries))
	}
	if again.Entries[0].Enabled {
		t.Error("用户的停用选择被同步覆盖了 —— 停用会变成无效操作")
	}
	if len(again.URLs) != 0 {
		t.Errorf("URLs = %v, want 空（唯一出口已被停用）", again.URLs)
	}
}

// TestSyncWarpProxyEntriesSkipsErrorInstances 创建失败的实例不该进池：
// 它没有可用的 socks 端口，进池只会让注册拿到一个必然失败的出口。
func TestSyncWarpProxyEntriesSkipsErrorInstances(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)

	insts := []WarpInstance{
		{Name: "ok", ProxyURL: "socks5://127.0.0.1:41000", Status: "running"},
		{Name: "bad", ProxyURL: "socks5://127.0.0.1:41001", Status: "error"},
		{Name: "empty", ProxyURL: "", Status: "running"},
	}
	pool, err := s.SyncWarpProxyEntries(ctx, insts)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if len(pool.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1（error 与空 URL 都要跳过）: %+v", len(pool.Entries), pool.Entries)
	}
}

// TestSyncWarpProxyEntriesIdempotent 反复同步不能产生重复条目。
// WARP 页每次刷新/新建都会调它，重复会让同一出口被算多次。
func TestSyncWarpProxyEntriesIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)
	insts := []WarpInstance{
		{Name: "w0", ProxyURL: "socks5://127.0.0.1:41000", Status: "running"},
		{Name: "w1", ProxyURL: "socks5://127.0.0.1:41001", Status: "running"},
	}
	for i := 0; i < 3; i++ {
		pool, err := s.SyncWarpProxyEntries(ctx, insts)
		if err != nil {
			t.Fatalf("第 %d 次同步失败: %v", i+1, err)
		}
		if len(pool.Entries) != 2 {
			t.Fatalf("第 %d 次同步后 Entries = %d, want 2（不能重复追加）", i+1, len(pool.Entries))
		}
	}
}
