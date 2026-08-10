package grokregister

// 代理工作集：按并发数取用代理，而不是每次都把整池拉起来预检。
//
// 背景：PreCheckProxyPool 会经每个代理各发一次探测请求。住宅代理普遍按请求数
// 或流量计费，用户配了 37 个槽、并发只开 1，旧实现每次开跑都要探 37 次，
// 额度全烧在预检上，且真正注册只用得到 1 个。
//
// 现在：工作集 = 并发数 + headroom（默认 0，即严格按并发数），其余进储备。
// 储备不预检、不消耗额度，只有工作集里的槽被淘汰时才即时补位。

import "strings"

// splitProxyWorkingSet 把代理池切成「工作集」与「储备」。
// concurrency <= 0 视为 1。cfg 为 nil 时用默认 headroom(0)。
// 池子本身不足时，工作集就是全部，储备为空。
func splitProxyWorkingSet(pool []string, concurrency int, cfg *BatchConfig) (working, reserve []string) {
	// 先去空去重，保持首次出现顺序（与 PreCheckProxyPool 的语义一致）
	unique := make([]string, 0, len(pool))
	seen := make(map[string]struct{}, len(pool))
	for _, u := range pool {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		unique = append(unique, u)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	// 显式要求预检全池 → 不切
	if cfg != nil && cfg.ProxyPrecheckAll {
		return unique, nil
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	want := concurrency
	if cfg != nil && cfg.ProxyWorkingHeadroom > 0 {
		want += cfg.ProxyWorkingHeadroom
	}
	if want >= len(unique) {
		return unique, nil
	}
	// 复制切片，避免调用方后续 append 影响到储备段
	working = append([]string(nil), unique[:want]...)
	reserve = append([]string(nil), unique[want:]...)
	return working, reserve
}

// takeProxyReserve 从储备头部取一个可用代理补位。
// 会对候选做一次实探（储备此前未预检），不可用就继续往后取。
// 返回补位代理与剩余储备；取不到时返回空串。
func takeProxyReserve(reserve []string) (picked string, rest []string) {
	for i := 0; i < len(reserve); i++ {
		cand := strings.TrimSpace(reserve[i])
		if cand == "" {
			continue
		}
		if ip, ok := testProxyAlive(cand); ok {
			putCachedProxy(cand, true, ip)
			return cand, reserve[i+1:]
		}
		putCachedProxy(cand, false, "")
		Logf("[Grok] [代理补位] 储备槽 %s 不可用，跳过", MaskProxy(cand))
	}
	return "", nil
}
