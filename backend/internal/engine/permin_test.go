package engine

import (
	"testing"
	"time"
)

// TestPerMinSampling 验证每分钟成功/失败采样：跨分钟时记录增量，
// 同一分钟只更新基准；环形保留最近 30 分钟。
func TestPerMinSampling(t *testing.T) {
	e := &RegisterEngine{}

	// 第 0 分钟基线：成功 0 失败 0
	e.flushPerMinLocked(1000, 0, 0)
	if len(e.perMin) != 0 {
		t.Fatalf("基线分钟不应产生条目，got %d", len(e.perMin))
	}

	// 同一分钟继续跑：成功 5，失败 1 —— 不产生条目
	e.flushPerMinLocked(1000, 5, 1)
	if len(e.perMin) != 0 {
		t.Fatalf("同一分钟不应产生条目，got %d", len(e.perMin))
	}

	// 跨到下一分钟：成功 12（增量 7），失败 2（增量 1）
	e.flushPerMinLocked(1001, 12, 2)
	if len(e.perMin) != 1 {
		t.Fatalf("跨分钟应产生 1 条，got %d", len(e.perMin))
	}
	if e.perMin[0].Success != 7 || e.perMin[0].Fail != 1 {
		t.Fatalf("增量错误: success=%d want 7, fail=%d want 1", e.perMin[0].Success, e.perMin[0].Fail)
	}

	// 再跨一分钟：成功 12 → 17（增量 5），失败 2 → 6（增量 4）
	e.flushPerMinLocked(1002, 17, 6)
	if len(e.perMin) != 2 {
		t.Fatalf("应有 2 条，got %d", len(e.perMin))
	}
	if e.perMin[1].Success != 5 || e.perMin[1].Fail != 4 {
		t.Fatalf("第二条增量错误: success=%d want 5, fail=%d want 4", e.perMin[1].Success, e.perMin[1].Fail)
	}

	// 环形上限 30：灌 40 个分钟
	e.resetPerMin()
	for m := int64(0); m < 40; m++ {
		e.flushPerMinLocked(2000+m, int(m), int(m/2))
	}
	if len(e.perMin) != 30 {
		t.Fatalf("环形应保留 30 条，got %d", len(e.perMin))
	}
	if e.perMin[0].Minute != 2000+10 {
		t.Fatalf("最旧条目应被丢弃，got minute=%d", e.perMin[0].Minute)
	}

	// reset 清空
	e.resetPerMin()
	if len(e.perMin) != 0 || e.perMinLast != 0 {
		t.Fatalf("reset 后应清空，got len=%d last=%d", len(e.perMin), e.perMinLast)
	}
}

// TestPerMinStatusExposed 验证 Status() 把 perMin 带出（浅拷贝，不共享底层数组）。
func TestPerMinStatusExposed(t *testing.T) {
	e := &RegisterEngine{}
	e.flushPerMinLocked(time.Now().Unix()/60-1, 5, 1)
	e.flushPerMinLocked(time.Now().Unix()/60, 8, 1)

	st := e.Status(nil)
	if len(st.PerMin) != 1 {
		t.Fatalf("Status 应带出 1 条 per_min，got %d", len(st.PerMin))
	}
	// 修改返回值的 slice 不应影响内部状态
	st.PerMin = nil
	if len(e.perMin) != 1 {
		t.Fatalf("Status 返回的 slice 应与内部隔离，内部应为 1 条，got %d", len(e.perMin))
	}
}
