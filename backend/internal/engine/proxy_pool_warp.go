package engine

import (
	"context"
	"strings"
)

// ── WARP 出口并入代理池 ──
//
// 为什么需要这一层：WARP 实例原来只把自己的 socks URL 追加进
// ProxySettings.RegisterURLs，而代理池页面渲染的是 ProxyPool.Entries ——
// 两套数据，所以 WARP 从来不在代理池里显示。更糟的是代理池页的「出口设置」
// 自动保存不发 register_urls（前端注释说「由代理池维护」），Go 侧收到 nil，
// cleanURLs(nil) 返回空切片，于是每次自动保存都把 WARP 加进去的 URL 抹掉一次。
// 实测：一个 running 的 WARP 实例（socks5://127.0.0.1:41000）在 register_urls
// 里完全找不到。
//
// 现在改成让 WARP 成为 Entries 里的一等条目（Source=warp）：
//   - 代理池页立刻可见、可单独启停
//   - 注册消费 pool.URLs（由启用条目算出）自然包含 WARP
//   - 容器删除时同步移除条目，不留僵尸
//   - 不再需要 register_urls 那条平行通路，双写冲突随之消失

// SyncWarpProxyEntries 让代理池里的 WARP 条目与当前 WARP 实例集合一致。
//
// 语义（幂等，可反复调用）：
//   - 实例存在而池里没有 → 追加，默认启用
//   - 池里有而实例已不存在 → 移除（容器被删）
//   - 两边都有 → 保留用户的启用/禁用选择与备注，只刷新 note 里的实例名
//
// 手动添加的条目（Source 为空）一律不动。
func (s *AccountService) SyncWarpProxyEntries(ctx context.Context, insts []WarpInstance) (ProxyPool, error) {
	pool, err := s.GetProxyPool(ctx)
	if err != nil {
		return pool, err
	}

	// 当前活跃的 WARP 出口：url → 展示名
	live := map[string]string{}
	for _, in := range insts {
		u := strings.TrimSpace(in.ProxyURL)
		if u == "" || in.Status == "error" {
			continue
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = strings.TrimSpace(in.Container)
		}
		live[u] = name
	}

	out := make([]ProxyEntry, 0, len(pool.Entries)+len(live))
	seen := map[string]struct{}{}
	for _, e := range pool.Entries {
		if e.Source != ProxySourceWarp {
			out = append(out, e) // 手动条目原样保留
			continue
		}
		name, ok := live[e.URL]
		if !ok {
			continue // 容器已删 → 丢弃这条，避免僵尸出口参与注册
		}
		e.Note = warpEntryNote(name)
		out = append(out, e)
		seen[e.URL] = struct{}{}
	}
	// 新实例追加。默认启用：用户点了「部署 WARP」就是想用它。
	for _, in := range insts {
		u := strings.TrimSpace(in.ProxyURL)
		if u == "" {
			continue
		}
		if _, ok := live[u]; !ok {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		// 与手动条目撞 URL 时不追加：尊重用户已有的那条（含其启停选择）
		if hasProxyURL(out, u) {
			continue
		}
		out = append(out, ProxyEntry{
			URL:     u,
			Note:    warpEntryNote(live[u]),
			Enabled: true,
			Source:  ProxySourceWarp,
		})
		seen[u] = struct{}{}
	}

	return s.SaveProxyPool(ctx, ProxyPool{Entries: out})
}

// warpEntryNote 代理池里 WARP 条目的备注。带前缀是为了在纯文本导出/日志里
// 也能一眼看出这是 WARP 出口，而不只靠 UI 标记。
func warpEntryNote(name string) string {
	if name == "" {
		return "WARP 出口"
	}
	return "WARP · " + name
}

// hasProxyURL 列表里是否已有该 URL。
func hasProxyURL(list []ProxyEntry, url string) bool {
	for _, e := range list {
		if e.URL == url {
			return true
		}
	}
	return false
}
