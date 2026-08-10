package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 入库转换的实时进度。
//
// 为什么需要：ConvertToOAuth 是一次阻塞调用，23 个账号按并发 4、每个最多
// convertMaxAttempts 次 attempt²×2s 退避，最坏要几分钟才返回。原来前端只能把
// 按钮切成「入库转换中…」然后干等，既看不到跑到第几个，也看不出是在退避重试
// 还是已经卡死 —— 用户只能猜。所以把进度单独暴露出来供轮询。

// ImportProgress 是入库转换的进度快照。
type ImportProgress struct {
	Running   bool     `json:"running"`
	Total     int      `json:"total"`
	Done      int      `json:"done"` // 已出结果（成功+失败）
	Success   int      `json:"success"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Current   string   `json:"current,omitempty"` // 正在转换的邮箱（多并发时取最近一个）
	Stage     string   `json:"stage"`             // idle|preparing|converting|saving|done|failed
	Message   string   `json:"message,omitempty"`
	Logs      []string `json:"logs,omitempty"`
	StartedAt string   `json:"started_at,omitempty"`
	ElapsedMS int64    `json:"elapsed_ms"`
}

const importProgressLogCap = 40

type importProgressTracker struct {
	mu   sync.Mutex
	p    ImportProgress
	from time.Time
}

func (t *importProgressTracker) begin(total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.from = time.Now()
	t.p = ImportProgress{
		Running:   true,
		Total:     total,
		Stage:     "preparing",
		Message:   fmt.Sprintf("准备转换 %d 个账号…", total),
		StartedAt: t.from.UTC().Format(time.RFC3339),
	}
}

func (t *importProgressTracker) stage(stage, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p.Stage = stage
	if msg != "" {
		t.p.Message = msg
	}
}

// startOne 标记某个账号开始转换（多并发下 Current 表示最近开始的那个）。
func (t *importProgressTracker) startOne(email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p.Stage = "converting"
	t.p.Current = email
}

// finishOne 记一个结果。ok=false 时 detail 是失败原因。
func (t *importProgressTracker) finishOne(email string, ok bool, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p.Done++
	if ok {
		t.p.Success++
		t.appendLogLocked(fmt.Sprintf("[%d/%d] ✓ %s", t.p.Done, t.p.Total, email))
	} else {
		t.p.Failed++
		t.appendLogLocked(fmt.Sprintf("[%d/%d] ✗ %s — %s", t.p.Done, t.p.Total, email, truncStr(detail, 120)))
	}
	t.p.Message = fmt.Sprintf("已完成 %d/%d（成功 %d，失败 %d）",
		t.p.Done, t.p.Total, t.p.Success, t.p.Failed)
}

func (t *importProgressTracker) addSkipped(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p.Skipped += n
}

func (t *importProgressTracker) log(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appendLogLocked(line)
}

func (t *importProgressTracker) appendLogLocked(line string) {
	t.p.Logs = append(t.p.Logs, time.Now().Format("15:04:05")+" "+line)
	if len(t.p.Logs) > importProgressLogCap {
		t.p.Logs = t.p.Logs[len(t.p.Logs)-importProgressLogCap:]
	}
}

func (t *importProgressTracker) end(res ConvertResult, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p.Running = false
	t.p.ElapsedMS = time.Since(t.from).Milliseconds()
	t.p.Current = ""
	if err != nil {
		t.p.Stage = "failed"
		t.p.Message = "入库失败：" + err.Error()
		return
	}
	t.p.Stage = "done"
	t.p.Success = res.Updated
	t.p.Failed = len(res.Failed)
	t.p.Skipped = res.Skipped
	// Done 以最终结果对齐 Total：被跳过的账号（无 SSO / 转换期间被删）
	// 不会走 finishOne，否则进度条永远差最后几格，看着像没跑完。
	if done := res.Updated + len(res.Failed) + res.Skipped; done > t.p.Done {
		t.p.Done = done
	}
	if t.p.Done > t.p.Total {
		t.p.Done = t.p.Total
	}
	t.p.Message = fmt.Sprintf("入库完成：成功 %d，失败 %d，跳过 %d（耗时 %.1fs）",
		res.Updated, len(res.Failed), res.Skipped, float64(t.p.ElapsedMS)/1000)
}

func (t *importProgressTracker) snapshot() ImportProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.p
	// 零值 ImportProgress 的 Stage 是空字符串，前端 stageLabel 会显示成空白。
	// 从未跑过任务时应明确是 idle。
	if p.Stage == "" {
		p.Stage = "idle"
	}
	if p.Running {
		p.ElapsedMS = time.Since(t.from).Milliseconds()
	}
	logs := make([]string, len(p.Logs))
	copy(logs, p.Logs)
	p.Logs = logs
	return p
}

// CountPendingImports 统计「待入库」账号数：有 SSO 但还没拿到 OAuth 凭证。
//
// 判定条件与 ConvertToOAuth 的分流逻辑严格一致（AccessToken 为空 且 SSO 非空）——
// 那里 AccessToken 非空会被直接标记已入库、SSO 为空会被 Skipped。两处若不一致，
// 注册页会显示「待入库 5」而点进去只转换 3 个，用户以为丢了账号。
//
// 接收已加载的列表而不是自己去 List()：调用方（/status 处理器）已经为
// accounts_count 加载过一次，而 List() 每次都要读 store + json.Unmarshal +
// 按 CreatedAt 排序全部账号。status 是被前端高频轮询的接口，再加载一次
// 等于把这份开销翻倍。
func CountPendingImports(list []Account) int {
	n := 0
	for i := range list {
		if strings.TrimSpace(list[i].AccessToken) == "" && strings.TrimSpace(list[i].SSO) != "" {
			n++
		}
	}
	return n
}

// ImportProgress 返回当前入库进度（前端轮询）。
func (s *AccountService) ImportProgress() ImportProgress {
	if s.importProg == nil {
		return ImportProgress{Stage: "idle"}
	}
	return s.importProg.snapshot()
}

// ConvertToOAuthAsync 异步入库：立刻返回，进度经 ImportProgress() 轮询。
//
// 拒绝并发启动：同一批账号被两次转换会各自向上游要一次 OAuth，
// 既浪费配额也可能让先完成的那次结果被后完成的覆盖。
func (s *AccountService) ConvertToOAuthAsync(ids []string, proxyMode string) (ImportProgress, error) {
	if s.importProg == nil {
		s.importProg = &importProgressTracker{}
	}
	if s.importProg.snapshot().Running {
		return s.importProg.snapshot(), fmt.Errorf("已有入库任务在进行中，请等它结束")
	}
	if len(ids) == 0 {
		return s.importProg.snapshot(), fmt.Errorf("没有待入库的账号")
	}
	s.importProg.begin(len(ids))
	go func() {
		// 用独立 context：HTTP 请求早就返回了，不能被它的取消带走
		res, err := s.ConvertToOAuth(context.Background(), ids, proxyMode)
		s.importProg.end(res, err)
	}()
	return s.importProg.snapshot(), nil
}
