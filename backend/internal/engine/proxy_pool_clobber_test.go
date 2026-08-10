package engine

import (
	"context"
	"testing"
)

// TestSaveProxySettingsMustNotClobberPool 复现并钉死用户报的
// 「添加的代理，切换导航再切回来就全没了」。
//
// 根因：SaveProxySettings 为了同步 legacy key，直接
//
//	json.Marshal(ProxyPool{URLs: ps.RegisterURLs})
//
// 覆写整个 KeyProxyPool —— Entries（URL+备注+启用状态）被整体丢弃。
//
// 代理池页面有两个 autosave（池子列表 → SaveProxyPool；出口设置 →
// SaveProxySettings）。切换导航时后者触发，把前者刚存好的 46 条代理清空。
// 而 RegisterURLs 通常为空（用户没单独配注册出口），于是连 urls 都变成 0，
// 表现为「什么都没保存」。
func TestSaveProxySettingsMustNotClobberPool(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)

	// 1) 用户在代理池页添加 3 条带备注的代理（等价于 PUT /proxy-pool）
	want := []ProxyEntry{
		{URL: "socks5://192.168.8.141:20312", Note: "日本Z05 | 下载专用 | x0.01", Enabled: true},
		{URL: "socks5://192.168.8.141:44679", Note: "香港Z09 | IEPL", Enabled: true},
		{URL: "socks5://192.168.8.141:36440", Note: "乌克兰Z01", Enabled: true},
	}
	if _, err := s.SaveProxyPool(ctx, ProxyPool{Entries: want}); err != nil {
		t.Fatalf("SaveProxyPool 失败: %v", err)
	}

	// 2) 切换导航 → 出口设置 autosave 触发。RegisterURLs 为空是常态：
	//    用户并没有单独配「注册出口」。
	if _, err := s.SaveProxySettings(ctx, ProxySettings{}); err != nil {
		t.Fatalf("SaveProxySettings 失败: %v", err)
	}

	// 3) 切回代理池页 → GET /proxy-pool。代理必须还在。
	got, err := s.GetProxyPool(ctx)
	if err != nil {
		t.Fatalf("GetProxyPool 失败: %v", err)
	}
	if len(got.Entries) != len(want) {
		t.Fatalf("保存出口设置后代理池被清空：entries=%d, want %d —— "+
			"这就是「切换导航回来代理全没了」", len(got.Entries), len(want))
	}
	for i, e := range want {
		if got.Entries[i].URL != e.URL {
			t.Errorf("第 %d 条 URL = %q, want %q", i, got.Entries[i].URL, e.URL)
		}
		// 备注同样不能丢：用户靠它区分节点（地区/线路/倍率）
		if got.Entries[i].Note != e.Note {
			t.Errorf("第 %d 条备注 = %q, want %q", i, got.Entries[i].Note, e.Note)
		}
		if !got.Entries[i].Enabled {
			t.Errorf("第 %d 条 Enabled 丢失", i)
		}
	}
}

// TestSaveProxySettingsWithRegisterURLsKeepsEntries 配了注册出口时，
// URLs 镜像要更新，但 Entries 仍然必须保留。
func TestSaveProxySettingsWithRegisterURLsKeepsEntries(t *testing.T) {
	ctx := context.Background()
	s := newTestAccountService(t)

	entries := []ProxyEntry{
		{URL: "socks5://a:1080", Note: "备注A", Enabled: true},
		{URL: "socks5://b:1080", Note: "备注B", Enabled: false},
	}
	if _, err := s.SaveProxyPool(ctx, ProxyPool{Entries: entries}); err != nil {
		t.Fatalf("SaveProxyPool 失败: %v", err)
	}

	regs := []string{"socks5://a:1080"}
	if _, err := s.SaveProxySettings(ctx, ProxySettings{RegisterURLs: regs}); err != nil {
		t.Fatalf("SaveProxySettings 失败: %v", err)
	}

	got, err := s.GetProxyPool(ctx)
	if err != nil {
		t.Fatalf("GetProxyPool 失败: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Errorf("Entries = %d, want 2（配了注册出口也不能丢池子）", len(got.Entries))
	}
	if len(got.URLs) != 1 || got.URLs[0] != regs[0] {
		t.Errorf("URLs 镜像 = %v, want %v", got.URLs, regs)
	}
	// 禁用状态必须保留：被镜像覆盖会让停用的代理重新参与注册
	for _, e := range got.Entries {
		if e.URL == "socks5://b:1080" && e.Enabled {
			t.Error("被停用的代理 b 变回启用 —— 停用状态被镜像覆盖了")
		}
	}
}
