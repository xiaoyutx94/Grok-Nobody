package grokregister

import (
	"testing"
	"time"
)

func TestAutoPauseTrackerBelowThresholdNoPause(t *testing.T) {
	tr := newAutoPauseTracker(10, 5)
	now := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 9; i++ {
		if tr.recordResult(false, now) {
			t.Fatalf("第 %d 次失败不应触发暂停", i+1)
		}
	}
}

func TestAutoPauseTrackerTriggersAtThreshold(t *testing.T) {
	tr := newAutoPauseTracker(3, 5)
	now := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	tr.recordResult(false, now)
	tr.recordResult(false, now)
	if !tr.recordResult(false, now) {
		t.Fatal("第 3 次连续失败应触发暂停")
	}
	tr.mu.Lock()
	until := tr.pauseUntil
	tr.mu.Unlock()
	want := now.Add(5 * time.Minute)
	if !until.Equal(want) {
		t.Fatalf("pauseUntil = %v, want %v", until, want)
	}
}

func TestAutoPauseTrackerSuccessResetsStreak(t *testing.T) {
	tr := newAutoPauseTracker(3, 5)
	now := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	tr.recordResult(false, now)
	tr.recordResult(false, now)
	tr.recordResult(true, now) // 成功清零
	if tr.recordResult(false, now) {
		t.Fatal("成功清零后单次失败不应触发暂停")
	}
	tr.recordResult(false, now)
	if !tr.recordResult(false, now) {
		t.Fatal("清零后重新连续 3 次失败应触发暂停")
	}
}

func TestAutoPauseTrackerNoReentryWhilePaused(t *testing.T) {
	tr := newAutoPauseTracker(2, 5)
	now := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	tr.recordResult(false, now)
	tr.recordResult(false, now) // 进入暂停窗口
	if tr.recordResult(false, now.Add(time.Minute)) {
		t.Fatal("暂停窗口内失败不应再次触发新暂停")
	}
}

func TestAutoPauseTrackerContinuesAfterWindow(t *testing.T) {
	tr := newAutoPauseTracker(2, 5)
	now := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	tr.recordResult(false, now)
	tr.recordResult(false, now) // pause until 14:05, streak 已清零
	after := now.Add(5*time.Minute + time.Second)
	tr.recordResult(false, after) // 窗口结束后重新累计
	if !tr.recordResult(false, after) {
		t.Fatal("窗口结束后连续 2 次失败应可再次触发暂停")
	}
}

func TestAutoPauseTrackerDefaults(t *testing.T) {
	tr := newAutoPauseTracker(0, 0)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.threshold != 10 {
		t.Fatalf("threshold 默认应为 10, got %d", tr.threshold)
	}
	if tr.wait != 5*time.Minute {
		t.Fatalf("wait 默认应为 5 分钟, got %v", tr.wait)
	}
}

func TestAutoPauseTrackerWaitIfPausedNotPaused(t *testing.T) {
	tr := newAutoPauseTracker(3, 5)
	if tr.waitIfPaused(nil) {
		t.Fatal("未暂停时 waitIfPaused 应直接返回 false")
	}
}
