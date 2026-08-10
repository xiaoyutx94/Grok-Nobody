package grokregister

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaskProxy hides credentials in proxy URLs for safe logging (1:1 with Kiro maskProxy).
func MaskProxy(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return "直连"
	}
	if idx := strings.Index(proxy, "@"); idx > 0 {
		schemeEnd := strings.Index(proxy, "://")
		if schemeEnd > 0 {
			return proxy[:schemeEnd+3] + "***@" + proxy[idx+1:]
		}
		return "***@" + proxy[idx+1:]
	}
	return proxy
}

func looksLikeIP(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	if strings.ContainsAny(s, " <>\"'") {
		return false
	}
	return len(s) >= 4 && len(s) <= 45
}

// proxyCheckTargets 是出口检测目标（多协议多地域，任一成功即判定可用）。
// 判定原则：通过代理成功完成一次 HTTP(S) 请求 = 可用；出口 IP 尽力提取，
// 提取不到不判失败（避免内容被劫持/验证页导致误杀可用代理）。
var proxyCheckTargets = []string{
	"https://www.cloudflare.com/cdn-cgi/trace", // 输出 ip= 行，全球可达
	"http://api4.ipify.org",                    // 纯文本 IP
	"http://icanhazip.com",                     // 纯文本 IP
	"http://ip.3322.net",                       // 国内可达 IP 文本
	"http://myip.ipip.net",                     // 国内
	"https://api.ipify.org",
	"http://members.3322.org/dyndns/getip",
}

// extractExitIPFromBody 从响应体尽力提取出口 IP（纯文本或 cdn-cgi/trace 的 ip= 行）。
func extractExitIPFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "ip="))
			if looksLikeIP(v) {
				return v
			}
			continue
		}
		// 纯文本 IP 行
		if looksLikeIP(line) {
			return line
		}
	}
	return ""
}

// testProxyAlive checks reachability through proxy. Returns exit IP and ok.
// 可用性判定：任一目标通过代理返回 2xx 且响应体非空即视为可用（连接成功），
// 出口 IP 提取失败不影响可用性判定。
// 目标并发尝试 + 总预算：不可达代理不会因 7 个目标串行超时（最坏 42s）拖慢批量检测。
func testProxyAlive(proxyURL string) (string, bool) {
	client := makeProxyHTTPClient(proxyURL, 10*time.Second)
	if client == nil {
		return "", false
	}
	// 总预算：目标并发尝试，任一成功即返回；全部失败按预算提前结束。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type probeResult struct {
		ip string
		ok bool
	}
	results := make(chan probeResult, len(proxyCheckTargets))
	var wg sync.WaitGroup
	for _, testURL := range proxyCheckTargets {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if rerr != nil {
				return
			}
			req.Header.Set("Accept", "text/plain, */*")
			req.Header.Set("User-Agent", "sub2api-proxy-check/1")
			resp, gerr := client.Do(req)
			if gerr != nil {
				return
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 && strings.TrimSpace(string(body)) != "" {
				results <- probeResult{ip: extractExitIPFromBody(string(body)), ok: true}
			}
		}(testURL)
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	for r := range results {
		if r.ok {
			return r.ip, true
		}
	}
	return "", false
}

// Proxy precheck cache: skip re-probing recently-alive URLs (short TTL).
// Dead results are NOT cached long — always retest failures next batch.
const (
	proxyAliveCacheTTL = 10 * time.Minute
	proxyDeadCacheTTL  = 45 * time.Second
)

type proxyCacheEntry struct {
	alive bool
	ip    string
	at    time.Time
}

var (
	proxyCacheMu sync.Mutex
	proxyCache   = map[string]proxyCacheEntry{}
)

func getCachedProxy(url string) (proxyCacheEntry, bool) {
	proxyCacheMu.Lock()
	defer proxyCacheMu.Unlock()
	e, ok := proxyCache[url]
	if !ok {
		return proxyCacheEntry{}, false
	}
	ttl := proxyAliveCacheTTL
	if !e.alive {
		ttl = proxyDeadCacheTTL
	}
	if time.Since(e.at) > ttl {
		delete(proxyCache, url)
		return proxyCacheEntry{}, false
	}
	return e, true
}

func putCachedProxy(url string, alive bool, ip string) {
	proxyCacheMu.Lock()
	defer proxyCacheMu.Unlock()
	proxyCache[url] = proxyCacheEntry{alive: alive, ip: ip, at: time.Now()}
	// bound map size
	if len(proxyCache) > 500 {
		// drop oldest half
		type kv struct {
			k string
			t time.Time
		}
		arr := make([]kv, 0, len(proxyCache))
		for k, v := range proxyCache {
			arr = append(arr, kv{k, v.at})
		}
		// simple: delete anything older than alive TTL
		now := time.Now()
		for k, v := range proxyCache {
			if now.Sub(v.at) > proxyAliveCacheTTL {
				delete(proxyCache, k)
			}
		}
		_ = arr
	}
}

// InvalidateProxyCache drops one URL (e.g. mid-batch fail).
func InvalidateProxyCache(url string) {
	proxyCacheMu.Lock()
	delete(proxyCache, strings.TrimSpace(url))
	proxyCacheMu.Unlock()
}

// PreCheckProxyPool tests proxies before batch starts (1:1 with Kiro preCheckProxyPool).
// Pool may contain duplicate URLs (same residential host, different named slots) — we test
// each unique URL once, then keep every original slot whose URL is alive so "全选 N 个"
// stays N slots after precheck (not collapsed to unique-URL count).
// Recently-alive URLs are served from a short TTL cache to cut startup cost on re-runs.
func PreCheckProxyPool(pool []string, maxRetries int, logPrefix string) []string {
	if len(pool) == 0 {
		return pool
	}
	if maxRetries < 1 {
		maxRetries = 1
	}
	if logPrefix == "" {
		logPrefix = "[Grok] "
	}

	// Unique URLs preserve first-seen order
	unique := make([]string, 0, len(pool))
	seen := make(map[string]struct{}, len(pool))
	for _, u := range pool {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		unique = append(unique, u)
	}

	// Split cache hits vs need probe
	aliveURL := make(map[string]string, len(unique)) // url → exit IP
	deadURL := make(map[string]struct{}, len(unique))
	toProbe := make([]string, 0, len(unique))
	cachedAlive, cachedDead := 0, 0
	for _, u := range unique {
		if e, ok := getCachedProxy(u); ok {
			if e.alive {
				aliveURL[u] = e.ip
				cachedAlive++
			} else {
				deadURL[u] = struct{}{}
				cachedDead++
			}
			continue
		}
		toProbe = append(toProbe, u)
	}

	Logf("%s[代理预检] 开始检测 %d 个代理槽（唯一 URL %d 个，缓存命中 alive=%d dead=%d，实检 %d）...",
		logPrefix, len(pool), len(unique), cachedAlive, cachedDead, len(toProbe))

	if len(toProbe) > 0 {
		type result struct {
			url   string
			alive bool
			ip    string
		}
		results := make(chan result, len(toProbe))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)

		for _, p := range toProbe {
			wg.Add(1)
			go func(proxyURL string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				for attempt := 1; attempt <= maxRetries; attempt++ {
					ip, ok := testProxyAlive(proxyURL)
					if ok {
						results <- result{proxyURL, true, ip}
						return
					}
					if attempt < maxRetries {
						time.Sleep(time.Duration(attempt) * time.Second)
					}
				}
				results <- result{proxyURL, false, ""}
			}(p)
		}
		go func() {
			wg.Wait()
			close(results)
		}()

		for r := range results {
			masked := MaskProxy(r.url)
			if r.alive {
				Logf("%s[代理预检] ✓ %s → %s", logPrefix, masked, r.ip)
				aliveURL[r.url] = r.ip
				putCachedProxy(r.url, true, r.ip)
			} else {
				Logf("%s[代理预检] ✗ %s 连续 %d 次失败, 已淘汰", logPrefix, masked, maxRetries)
				deadURL[r.url] = struct{}{}
				putCachedProxy(r.url, false, "")
			}
		}
	} else if cachedAlive > 0 {
		Logf("%s[代理预检] 全部命中缓存，跳过实检", logPrefix)
	}

	// Expand: keep every original slot whose URL survived.
	alive := make([]string, 0, len(pool))
	for _, u := range pool {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, dead := deadURL[u]; dead {
			continue
		}
		if ip, ok := aliveURL[u]; ok {
			_ = ip
			alive = append(alive, u)
		}
	}
	Logf("%s[代理预检] 完成: 槽位 %d/%d 可用（唯一 URL 存活 %d/%d，缓存命中 %d）",
		logPrefix, len(alive), len(pool), len(aliveURL), len(unique), cachedAlive+cachedDead)
	return alive
}

// VerifyProxyMidBatch checks a proxy during batch; returns true if alive.
func VerifyProxyMidBatch(proxyURL, logPrefix string) bool {
	_, ok := testProxyAlive(proxyURL)
	if !ok {
		Logf("%s[代理验证] ✗ %s 不可用", logPrefix, MaskProxy(proxyURL))
	}
	return ok
}

// TestProxyAlive 供外部包（engine）做单条代理连通/出口检测。
func TestProxyAlive(proxyURL string) (string, bool) {
	return testProxyAlive(proxyURL)
}
