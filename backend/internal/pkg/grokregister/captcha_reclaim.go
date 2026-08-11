package grokregister

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── 批次结束回收打码浏览器 ──
//
// 为什么需要：打码引擎（auralith context 池 / veloraturn / ezsolver）为了让下一
// 次 solve 不付 Chrome 冷启动，会保留 standby 进程常驻。注册跑完后这些 Chrome
// 一直挂着，实测容器内 14 个 chrome 进程、cgroup 2.3GB 常驻不还 —— 没有注册任务
// 时纯属浪费，且下次开跑前它们已经在啃内存，反而更容易触发资源闸门。
//
// 三个引擎都实现了 EzSolver 兼容的 POST /admin/release：
//   - {"suspend": true} = 硬回收：取消在飞 solve + 关闭全部 context host/浏览器
//     进程，并把池「停泊」住不自动补进程（下次 /solve 到达时自愈 resume）；
//   - 不传 suspend = 软回收：只关空闲池，standby 会被 warmer 立刻补回来。
//
// 批次结束要的是硬回收：进程一个不留。auralith 的 handleSolve 开头有
// `if contextPoolSuspended() { resumeContextPool() }`，所以下一批开跑会自动
// 恢复，不需要手动 prewarm，也不会「回收完就再也不打码」。

// captchaEndpoint 是一个可回收的打码引擎地址。
type captchaEndpoint struct {
	Name string
	URL  string
	Key  string
}

// captchaEndpoints 返回本批次实际用到的打码引擎地址（按 provider 收敛，
// 避免对没用过的引擎发无谓请求）。付费/远程通道没有本地浏览器可回收。
//
// VeloraTurn 与 EzSolver 共用 EzSolverURL 字段（同一套 /solve 协议，
// 切换 provider 时 UI 写的是同一个地址位），所以两者取同一来源。
func captchaEndpoints(cap CaptchaOptions) []captchaEndpoint {
	var out []captchaEndpoint
	add := func(name, url, key string) {
		url = strings.TrimSpace(strings.TrimRight(url, "/"))
		if url == "" {
			return
		}
		out = append(out, captchaEndpoint{Name: name, URL: url, Key: key})
	}
	switch cap.Provider {
	case CaptchaProviderAuralith:
		add("Auralith", cap.AuralithURL, cap.AuralithAPIKey)
	case CaptchaProviderEzSolver:
		add("EzSolver", cap.EzSolverURL, cap.EzSolverAPIKey)
	case CaptchaProviderVeloraTurn:
		add("VeloraTurn", cap.EzSolverURL, cap.EzSolverAPIKey)
	case CaptchaProviderAuto:
		// auto 会在 ezsolver → next → yes 之间回退，本地那一档要回收。
		add("EzSolver", cap.EzSolverURL, cap.EzSolverAPIKey)
		add("Auralith", cap.AuralithURL, cap.AuralithAPIKey)
	default:
		// 付费通道(yescaptcha/nextcaptcha)与 none 没有本地浏览器，无需回收。
	}
	return out
}

// ReleaseCaptchaBrowsers 对本批次用到的打码引擎做硬回收（关闭全部浏览器进程）。
//
// 失败只记日志不影响批次结果：回收是「省内存」而不是「保正确」，
// 引擎版本老、没有 /admin/release 时也应静默降级。
func ReleaseCaptchaBrowsers(ctx context.Context, cap CaptchaOptions) {
	eps := captchaEndpoints(cap)
	if len(eps) == 0 {
		return
	}
	for _, ep := range eps {
		released, err := releaseOne(ctx, ep.URL, ep.Key)
		switch {
		case err != nil:
			Logf("[Captcha] [%s] 空闲回收跳过: %v", ep.Name, err)
		case released > 0:
			Logf("[Captcha] [%s] 已回收 %d 个浏览器/上下文（无注册任务时不驻留进程）", ep.Name, released)
		default:
			Logf("[Captcha] [%s] 已回收（当前无驻留浏览器）", ep.Name)
		}
	}
}

// releaseOne 调 POST /admin/release {"suspend":true,"wait":true}，返回释放数量。
//
// wait=true 让引擎在返回前把进程真正 join 掉，这样调用方看到 200 就等于
// 「Chrome 已经没了」，而不是「已经发出关闭信号」——否则紧接着查内存会误判没生效。
func releaseOne(ctx context.Context, base, apiKey string) (int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"suspend": true, "wait": true})
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/admin/release", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := strings.TrimSpace(apiKey); k != "" {
		req.Header.Set("X-API-Key", k)
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("引擎无 /admin/release（版本较旧，升级后支持停止即释放浏览器）")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 160))
	}
	var out struct {
		Released       int `json:"released"`
		ReleasedActive int `json:"released_active"`
		ReleasedIdle   int `json:"released_idle"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Released > 0 {
		return out.Released, nil
	}
	return out.ReleasedActive + out.ReleasedIdle, nil
}
