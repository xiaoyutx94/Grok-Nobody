package engine

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestImportProgressReportsIncrementally 进度必须逐个推进。
// 原实现是一次阻塞 POST，23 个账号（并发 4、每个最多 3 次 attempt²×2s 退避）
// 最坏要几分钟才返回，界面上只有一个转圈按钮 —— 用户看不出跑到第几个，
// 也分不清是在退避重试还是已经卡死。
func TestImportProgressReportsIncrementally(t *testing.T) {
	tr := &importProgressTracker{}
	tr.begin(5)

	if p := tr.snapshot(); !p.Running || p.Total != 5 || p.Done != 0 {
		t.Fatalf("begin 后应为 running 0/5，实际 %+v", p)
	}

	seen := []int{}
	for i, email := range []string{"a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com"} {
		tr.startOne(email)
		if p := tr.snapshot(); p.Current != email {
			t.Errorf("startOne 后 Current 应为 %s，实际 %q", email, p.Current)
		}
		ok := i != 2 // 第 3 个失败
		detail := ""
		if !ok {
			detail = "429 Too Many Requests"
		}
		tr.finishOne(email, ok, detail)
		p := tr.snapshot()
		seen = append(seen, p.Done)
		if p.Done != i+1 {
			t.Errorf("第 %d 个完成后 Done 应为 %d，实际 %d", i+1, i+1, p.Done)
		}
	}

	// 必须是 1,2,3,4,5 而不是一次跳到 5
	for i, d := range seen {
		if d != i+1 {
			t.Errorf("进度未逐个推进: %v", seen)
			break
		}
	}

	p := tr.snapshot()
	if p.Success != 4 || p.Failed != 1 {
		t.Errorf("应为成功 4 失败 1，实际 成功%d 失败%d", p.Success, p.Failed)
	}
	// 失败原因要进日志，否则用户不知道为什么失败
	joined := strings.Join(p.Logs, "\n")
	if !strings.Contains(joined, "429") {
		t.Errorf("失败原因未进日志: %v", p.Logs)
	}
	if !strings.Contains(joined, "✓") || !strings.Contains(joined, "✗") {
		t.Errorf("日志应区分成功/失败: %v", p.Logs)
	}
}

// TestImportProgressEndReconcilesDone 被跳过的账号不走 finishOne，
// 如果不在收尾时对齐，进度条会永远差最后几格，看着像没跑完。
func TestImportProgressEndReconcilesDone(t *testing.T) {
	tr := &importProgressTracker{}
	tr.begin(10)
	// 只有 6 个真正跑了（4 成功 2 失败），另外 4 个被跳过
	for i := 0; i < 4; i++ {
		tr.finishOne("ok@x.com", true, "")
	}
	for i := 0; i < 2; i++ {
		tr.finishOne("bad@x.com", false, "no sso")
	}
	tr.end(ConvertResult{Updated: 4, Failed: []ConvertFailure{{}, {}}, Skipped: 4}, nil)

	p := tr.snapshot()
	if p.Running {
		t.Error("end 后不应仍为 running")
	}
	if p.Done != p.Total {
		t.Errorf("收尾后 Done 应对齐 Total，实际 %d/%d", p.Done, p.Total)
	}
	if p.Stage != "done" {
		t.Errorf("stage 应为 done，实际 %q", p.Stage)
	}
}

// TestImportProgressDoneNeverExceedsTotal 快路径与阶段二都会 finishOne，
// 重复计数会让 Done 超过 Total，进度条溢出 100%。
func TestImportProgressDoneNeverExceedsTotal(t *testing.T) {
	tr := &importProgressTracker{}
	tr.begin(2)
	for i := 0; i < 5; i++ {
		tr.finishOne("x@x.com", true, "")
	}
	tr.end(ConvertResult{Updated: 5, Skipped: 0, Failed: nil}, nil)
	if p := tr.snapshot(); p.Done > p.Total {
		t.Errorf("Done %d 超过 Total %d", p.Done, p.Total)
	}
}

// TestImportProgressIdleWhenNeverRun 零值 ImportProgress 的 Stage 是空字符串，
// 前端 stageLabel 会渲染成空白。从未跑过时必须明确是 idle。
func TestImportProgressIdleWhenNeverRun(t *testing.T) {
	s := NewAccountService(nil)
	p := s.ImportProgress()
	if p.Stage != "idle" {
		t.Errorf("从未跑过时 stage 应为 idle，实际 %q", p.Stage)
	}
	if p.Running {
		t.Error("从未跑过时不应为 running")
	}
}

// TestImportProgressRejectsConcurrentStart 同一批账号被两次转换会各自向上游
// 要一次 OAuth：既浪费配额，也可能让先完成的结果被后完成的覆盖。
func TestImportProgressRejectsConcurrentStart(t *testing.T) {
	s := NewAccountService(nil)
	s.importProg.begin(3) // 模拟已有任务在跑
	if _, err := s.ConvertToOAuthAsync([]string{"a"}, ""); err == nil {
		t.Error("已有任务在跑时应拒绝再启动")
	} else if !strings.Contains(err.Error(), "进行中") {
		t.Errorf("拒绝原因应说明已有任务在进行，实际: %v", err)
	}
	// 空 ID 列表也应拒绝
	s.importProg.end(ConvertResult{}, nil)
	if _, err := s.ConvertToOAuthAsync(nil, ""); err == nil {
		t.Error("空 ID 列表应拒绝")
	}
}

// TestImportProgressConcurrentSafe 阶段二是并发 4 调用 finishOne 的，
// 计数器竞争会导致 Done 少算。需 -race 才能稳定捕获。
func TestImportProgressConcurrentSafe(t *testing.T) {
	tr := &importProgressTracker{}
	const n = 200
	tr.begin(n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			tr.startOne("x@x.com")
			tr.finishOne("x@x.com", k%3 != 0, "boom")
			_ = tr.snapshot() // 读侧并发（前端轮询）
		}(i)
	}
	wg.Wait()
	if p := tr.snapshot(); p.Done != n {
		t.Errorf("并发计数丢失：Done=%d 期望 %d", p.Done, n)
	}
}

// TestImportProgressLogCap 日志不能无界增长（长批次会把内存和响应体撑大）。
func TestImportProgressLogCap(t *testing.T) {
	tr := &importProgressTracker{}
	tr.begin(500)
	for i := 0; i < 500; i++ {
		tr.finishOne("x@x.com", true, "")
	}
	if got := len(tr.snapshot().Logs); got > importProgressLogCap {
		t.Errorf("日志应封顶 %d，实际 %d", importProgressLogCap, got)
	}
}

// TestImportProgressElapsedAdvances 运行中 ElapsedMS 应随时间增长，
// 否则前端显示的耗时会一直是 0s。
func TestImportProgressElapsedAdvances(t *testing.T) {
	tr := &importProgressTracker{}
	tr.begin(1)
	time.Sleep(30 * time.Millisecond)
	if p := tr.snapshot(); p.ElapsedMS <= 0 {
		t.Errorf("运行中 ElapsedMS 应 >0，实际 %d", p.ElapsedMS)
	}
}
