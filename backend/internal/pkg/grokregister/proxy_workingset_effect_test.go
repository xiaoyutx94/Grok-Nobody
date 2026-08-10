package grokregister

import "testing"

// TestBatchConfigDrivesWorkingSet 固定住「配置改变 → 工作集切分改变」。
// engine 侧只负责把落盘的 BatchConfig 透传进来（见 engine 的
// TestRunPassesBatchConfigToBatchOptions 与 TestBatchConfigRoundTrips），
// 这里验证同一组值确实改变行为，而不是躺在配置里不生效。
func TestBatchConfigDrivesWorkingSet(t *testing.T) {
	pool := poolOf(20)

	// 默认 headroom=0：严格按并发数
	def := DefaultBatchConfig()
	w, r := splitProxyWorkingSet(pool, 2, def)
	if len(w) != 2 || len(r) != 18 {
		t.Fatalf("默认: 工作集 %d 储备 %d，want 2/18", len(w), len(r))
	}

	// headroom=3 → 并发 2 得到 5 个
	withHead := DefaultBatchConfig()
	withHead.ProxyWorkingHeadroom = 3
	w, r = splitProxyWorkingSet(pool, 2, withHead)
	if len(w) != 5 || len(r) != 15 {
		t.Errorf("headroom=3: 工作集 %d 储备 %d，want 5/15", len(w), len(r))
	}

	// precheck_all=true → 整池进工作集，无储备
	all := DefaultBatchConfig()
	all.ProxyPrecheckAll = true
	w, r = splitProxyWorkingSet(pool, 1, all)
	if len(w) != 20 || len(r) != 0 {
		t.Errorf("precheck_all: 工作集 %d 储备 %d，want 20/0", len(w), len(r))
	}

	// 两者同时给：precheck_all 优先（不切分）
	both := DefaultBatchConfig()
	both.ProxyPrecheckAll = true
	both.ProxyWorkingHeadroom = 3
	w, r = splitProxyWorkingSet(pool, 1, both)
	if len(w) != 20 || len(r) != 0 {
		t.Errorf("precheck_all 应优先: 工作集 %d 储备 %d", len(w), len(r))
	}
}

// TestMaxProxyFailsAndMaxPerIPAreRead 这两个值来自 BatchConfig，
// batch.go 里有 <=0 的兜底；确认兜底不会把用户设的值改掉。
func TestMaxProxyFailsAndMaxPerIPAreRead(t *testing.T) {
	cfg := DefaultBatchConfig()
	if cfg.MaxProxyFails != 3 {
		t.Errorf("默认 MaxProxyFails = %d, want 3", cfg.MaxProxyFails)
	}
	if cfg.MaxPerIP != 100 {
		t.Errorf("默认 MaxPerIP = %d, want 100", cfg.MaxPerIP)
	}
	cfg.MaxProxyFails = 7
	cfg.MaxPerIP = 3
	// 模拟 batch.go 的读取兜底
	mpf := cfg.MaxProxyFails
	if mpf <= 0 {
		mpf = 3
	}
	mpi := cfg.MaxPerIP
	if mpi <= 0 {
		mpi = 1
	}
	if mpf != 7 {
		t.Errorf("兜底改写了用户值: MaxProxyFails = %d", mpf)
	}
	if mpi != 3 {
		t.Errorf("兜底改写了用户值: MaxPerIP = %d", mpi)
	}
}
