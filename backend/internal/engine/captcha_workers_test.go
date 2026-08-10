package engine

import (
	"testing"
)

// TestResolveCaptchaWorkersStrictFollowsSettings 钉住 EzSolver/VeloraTurn 语义：
// 严格跟随设置值（与 sub2api 一致），0/未设置 → 1，不跟随注册并发——
// Auralith 才按注册并发 1:1（decideAuralithConcurrency 单独测）。
func TestResolveCaptchaWorkersStrictFollowsSettings(t *testing.T) {
	cases := []struct {
		name       string
		explicit   int
		concurrent int
		want       int
	}{
		// 0/未设置 → 1（sub2api: EzSolverMaxConcur <= 0 → 1）
		{"未设置 并发1", 0, 1, 1},
		{"未设置 并发10", 0, 10, 1},
		{"未设置 并发0", 0, 0, 1},
		// explicit 当闸
		{"上限8 并发1", 8, 1, 8},
		{"上限8 并发16", 8, 16, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveCaptchaWorkers(tc.explicit, tc.concurrent); got != tc.want {
				t.Errorf("resolveCaptchaWorkers(%d, %d) = %d, want %d",
					tc.explicit, tc.concurrent, got, tc.want)
			}
		})
	}
}

// TestDecideAuralithConcurrency 对齐 sub2api 的 decideAuralithConcurrency：
// explicit>0 → 闸=设置值；0/auto → 保留 live，仅在 live < 注册并发时扩容。
func TestDecideAuralithConcurrency(t *testing.T) {
	cases := []struct {
		name         string
		register     int
		explicit     int
		live         int
		wantGate     int
		wantWorkers  int
		wantSet      bool
	}{
		// 用户场景：10 并发、设置 0、live 20 → 保留 20（不降不扩）
		{"auto 10并发 live20", 10, 0, 20, 20, 20, false},
		// 服务器场景：6 并发、live 8 → 保留 8（web 版实际行为）
		{"auto 6并发 live8", 6, 0, 8, 8, 8, false},
		// live 低于注册并发 → 扩容到注册并发
		{"auto 10并发 live5 扩容", 10, 0, 5, 10, 10, true},
		// live 未知（0）→ 用 max(register, 4)
		{"auto 10并发 live0", 10, 0, 0, 10, 10, false},
		{"auto 2并发 live0 兜底4", 2, 0, 0, 4, 4, false},
		// explicit>0 → 闸=workers=设置值
		{"显式8 live20", 10, 8, 20, 8, 8, true},
		{"显式0即auto", 10, 0, 12, 12, 12, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideAuralithConcurrency(tc.register, tc.explicit, tc.live)
			if got.Gate != tc.wantGate || got.Workers != tc.wantWorkers || got.SetWorkers != tc.wantSet {
				t.Errorf("decideAuralithConcurrency(%d,%d,%d) = %+v, want gate=%d workers=%d set=%v",
					tc.register, tc.explicit, tc.live, got, tc.wantGate, tc.wantWorkers, tc.wantSet)
			}
		})
	}
}

// TestDecideAuralithConcurrencyClamped 并发钳制与 sub2api 一致（0..50）。
func TestDecideAuralithConcurrencyClamped(t *testing.T) {
	got := decideAuralithConcurrency(-1, -1, -1)
	if got.Gate < 1 || got.Gate > maxRegisterConcurrency {
		t.Errorf("负数钳制失败: %+v", got)
	}
	got = decideAuralithConcurrency(999, 999, 999)
	if got.Gate > maxRegisterConcurrency {
		t.Errorf("超上限未钳制: %+v", got)
	}
}

// fakeSink 记录下发到插件中心的并发值与自愈调用。
type fakeSink struct {
	got     map[string]int
	ensured []string
}

func (f *fakeSink) SetMaxWorkers(id string, n int) {
	if f.got == nil {
		f.got = map[string]int{}
	}
	f.got[id] = n
}

func (f *fakeSink) EnsureCaptchaReady(id string) (string, error) {
	f.ensured = append(f.ensured, id)
	return "", nil
}

// TestEnsureCaptchaReadyPicksProviderPlugin 自愈要作用在本次实际使用的打码器上。
// 走错插件的话，Docker 容器停掉的那个仍然连不上，整批注册照旧全失败。
func TestEnsureCaptchaReadyPicksProviderPlugin(t *testing.T) {
	cases := map[string]string{
		"auralith":   captchaPluginAuralith,
		"au":         captchaPluginAuralith,
		"veloraturn": captchaPluginVeloraTurn,
		"ezsolver":   captchaPluginEzSolver,
		"":           captchaPluginEzSolver,
		"auto":       captchaPluginEzSolver,
	}
	for provider, want := range cases {
		e := &RegisterEngine{}
		sink := &fakeSink{}
		e.SetCaptchaWorkerSink(sink)
		e.ensureCaptchaReady(provider)
		if len(sink.ensured) != 1 || sink.ensured[0] != want {
			t.Errorf("provider=%q 自愈了 %v，期望 [%s]", provider, sink.ensured, want)
		}
	}
}

// TestEnsureCaptchaReadyNilSinkIsSafe 没注入 sink 时不能 panic。
func TestEnsureCaptchaReadyNilSinkIsSafe(t *testing.T) {
	e := &RegisterEngine{}
	e.ensureCaptchaReady("auralith")
}

// TestPushCaptchaWorkersCoversAllThreeEngines 三个引擎都必须收到下发：
// Ez/Velora 严格跟随设置（8）；Auralith 0=auto 不预设（0，保留 live/auto-tune）。
func TestPushCaptchaWorkersCoversAllThreeEngines(t *testing.T) {
	e := &RegisterEngine{}
	sink := &fakeSink{}
	e.SetCaptchaWorkerSink(sink)
	e.pushCaptchaWorkers(Config{EzSolverMaxConcur: 8, AuralithMaxConcur: 0, DefaultConcurrency: 2}, 2)

	want := map[string]int{
		captchaPluginEzSolver:   8,
		captchaPluginVeloraTurn: 8,
		captchaPluginAuralith:   0, // 0=auto：不预设 MAX_WORKERS
	}
	for id, w := range want {
		if got, ok := sink.got[id]; !ok {
			t.Errorf("%s 没有收到打码并发下发", id)
		} else if got != w {
			t.Errorf("%s 收到 %d，期望 %d", id, got, w)
		}
	}
}

// TestPushCaptchaWorkersUsesDefaultConcurrencyWhenUnknown 保存设置时还不知道
// 本次注册并发，应退回配置里的默认并发（Ez 0 → 1，对齐 sub2api）。
func TestPushCaptchaWorkersUsesDefaultConcurrencyWhenUnknown(t *testing.T) {
	e := &RegisterEngine{}
	sink := &fakeSink{}
	e.SetCaptchaWorkerSink(sink)
	e.pushCaptchaWorkers(Config{EzSolverMaxConcur: 0, AuralithMaxConcur: 0, DefaultConcurrency: 4}, 0)
	if got := sink.got[captchaPluginEzSolver]; got != 1 {
		t.Errorf("EzSolver 0 应回落 1，实际 %d", got)
	}
}

// TestPushCaptchaWorkersNilSinkIsSafe 没注入 sink（单测/headless）时不能 panic。
func TestPushCaptchaWorkersNilSinkIsSafe(t *testing.T) {
	e := &RegisterEngine{}
	e.pushCaptchaWorkers(Config{DefaultConcurrency: 2}, 2)
}
