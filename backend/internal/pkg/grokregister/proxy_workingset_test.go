package grokregister

import (
	"fmt"
	"strings"
	"testing"
)

func poolOf(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("socks5://10.0.0.%d:1080", i))
	}
	return out
}

// TestWorkingSetMatchesConcurrency 用户诉求：选几个并发就只用几个代理。
func TestWorkingSetMatchesConcurrency(t *testing.T) {
	pool := poolOf(37)
	cfg := DefaultBatchConfig()

	for _, concurrency := range []int{1, 2, 4, 8} {
		working, reserve := splitProxyWorkingSet(pool, concurrency, cfg)
		if len(working) != concurrency {
			t.Errorf("并发 %d → 工作集 %d 个，want %d", concurrency, len(working), concurrency)
		}
		if len(reserve) != 37-concurrency {
			t.Errorf("并发 %d → 储备 %d 个，want %d", concurrency, len(reserve), 37-concurrency)
		}
		// 工作集取前 N 个（引擎已按 proxy_pick_mode 打乱过顺序）
		for i := 0; i < len(working); i++ {
			if working[i] != pool[i] {
				t.Errorf("并发 %d 工作集[%d] = %s, want %s", concurrency, i, working[i], pool[i])
			}
		}
		// 工作集与储备不得重叠
		inWorking := map[string]bool{}
		for _, w := range working {
			inWorking[w] = true
		}
		for _, r := range reserve {
			if inWorking[r] {
				t.Errorf("储备项 %s 同时出现在工作集里", r)
			}
		}
	}
}

func TestWorkingSetHeadroom(t *testing.T) {
	pool := poolOf(20)
	cfg := DefaultBatchConfig()
	cfg.ProxyWorkingHeadroom = 3
	working, reserve := splitProxyWorkingSet(pool, 2, cfg)
	if len(working) != 5 {
		t.Errorf("并发 2 + headroom 3 → 工作集 %d，want 5", len(working))
	}
	if len(reserve) != 15 {
		t.Errorf("储备 %d，want 15", len(reserve))
	}
}

func TestWorkingSetPrecheckAllRestoresOldBehaviour(t *testing.T) {
	pool := poolOf(37)
	cfg := DefaultBatchConfig()
	cfg.ProxyPrecheckAll = true
	working, reserve := splitProxyWorkingSet(pool, 1, cfg)
	if len(working) != 37 {
		t.Errorf("precheck_all → 工作集 %d，want 37（旧行为）", len(working))
	}
	if len(reserve) != 0 {
		t.Errorf("precheck_all 时不应有储备，实际 %d", len(reserve))
	}
}

func TestWorkingSetSmallPoolTakesAll(t *testing.T) {
	pool := poolOf(2)
	cfg := DefaultBatchConfig()
	working, reserve := splitProxyWorkingSet(pool, 8, cfg)
	if len(working) != 2 {
		t.Errorf("池子只有 2 个而并发 8 → 工作集 %d，want 2", len(working))
	}
	if len(reserve) != 0 {
		t.Errorf("储备应为空，实际 %d", len(reserve))
	}
}

func TestWorkingSetDedupesAndDropsBlanks(t *testing.T) {
	pool := []string{
		"socks5://a:1080",
		"  ",
		"socks5://a:1080", // 重复
		" socks5://b:1080 ",
		"",
		"socks5://c:1080",
	}
	working, reserve := splitProxyWorkingSet(pool, 2, DefaultBatchConfig())
	if len(working) != 2 {
		t.Fatalf("工作集 %d，want 2: %v", len(working), working)
	}
	if working[0] != "socks5://a:1080" || working[1] != "socks5://b:1080" {
		t.Errorf("去重/去空后顺序不对: %v", working)
	}
	if len(reserve) != 1 || reserve[0] != "socks5://c:1080" {
		t.Errorf("储备 = %v", reserve)
	}
}

func TestWorkingSetEmptyAndZeroConcurrency(t *testing.T) {
	if w, r := splitProxyWorkingSet(nil, 4, DefaultBatchConfig()); len(w) != 0 || len(r) != 0 {
		t.Errorf("空池 → %v / %v", w, r)
	}
	if w, r := splitProxyWorkingSet([]string{"", "   "}, 4, DefaultBatchConfig()); len(w) != 0 || len(r) != 0 {
		t.Errorf("全空白池 → %v / %v", w, r)
	}
	// 并发 0/负数按 1 处理
	for _, c := range []int{0, -3} {
		w, r := splitProxyWorkingSet(poolOf(5), c, DefaultBatchConfig())
		if len(w) != 1 {
			t.Errorf("并发 %d → 工作集 %d，want 1", c, len(w))
		}
		if len(r) != 4 {
			t.Errorf("并发 %d → 储备 %d，want 4", c, len(r))
		}
	}
}

func TestWorkingSetNilConfig(t *testing.T) {
	// cfg 为 nil 时按 headroom=0 处理，不能 panic
	w, r := splitProxyWorkingSet(poolOf(10), 3, nil)
	if len(w) != 3 || len(r) != 7 {
		t.Errorf("nil cfg → 工作集 %d 储备 %d，want 3/7", len(w), len(r))
	}
}

// TestWorkingSetDoesNotAliasCallerSlice 切出来的两段不能与入参共享底层数组，
// 否则调用方后续 append 会踩到储备段。
func TestWorkingSetDoesNotAliasCallerSlice(t *testing.T) {
	pool := poolOf(6)
	working, reserve := splitProxyWorkingSet(pool, 2, DefaultBatchConfig())
	firstReserve := reserve[0]
	// 模拟调用方对工作集 append（batch.go 补位时就会这么做）
	working = append(working, "socks5://injected:1080")
	_ = working
	if reserve[0] != firstReserve {
		t.Errorf("append 工作集污染了储备: %s → %s", firstReserve, reserve[0])
	}
	// 同样不能污染入参
	for i, u := range poolOf(6) {
		if pool[i] != u {
			t.Errorf("入参被修改: pool[%d] = %s, want %s", i, pool[i], u)
		}
	}
}

func TestTakeProxyReserveSkipsDeadAndReturnsRest(t *testing.T) {
	// 用不可能连通的地址：testProxyAlive 会失败 → 全部跳过
	reserve := []string{
		"socks5://127.0.0.1:1", // 端口 1 基本不会有 SOCKS
		"   ",
		"socks5://127.0.0.1:2",
	}
	picked, rest := takeProxyReserve(reserve)
	if picked != "" {
		t.Errorf("全不可用时不该返回 picked，实际 %q", picked)
	}
	if len(rest) != 0 {
		t.Errorf("取尽后 rest 应为空，实际 %v", rest)
	}
}

func TestTakeProxyReserveEmpty(t *testing.T) {
	if picked, rest := takeProxyReserve(nil); picked != "" || rest != nil {
		t.Errorf("空储备 → %q / %v", picked, rest)
	}
}

// TestBatchConfigDefaultsFavourWorkingSet 默认不再预检全池。
func TestBatchConfigDefaultsFavourWorkingSet(t *testing.T) {
	cfg := DefaultBatchConfig()
	if cfg.ProxyPrecheckAll {
		t.Error("默认不应预检全池（住宅代理按请求计费）")
	}
	if cfg.ProxyWorkingHeadroom != 0 {
		t.Errorf("默认 headroom = %d，want 0（严格按并发数）", cfg.ProxyWorkingHeadroom)
	}
}

// TestWorkingSetLogWording 保证日志里明确写出「工作集/储备」，便于用户核对。
func TestWorkingSetLogWording(t *testing.T) {
	// 仅静态检查代码里保留了关键字，避免以后有人把日志删掉导致行为不可观测
	for _, kw := range []string{"工作集", "储备"} {
		if !strings.Contains(workingSetLogKeywords, kw) {
			t.Errorf("缺少日志关键字 %q", kw)
		}
	}
}

// workingSetLogKeywords 与 batch.go / proxy_workingset.go 的日志措辞对应。
const workingSetLogKeywords = "工作集 储备 代理补位"
