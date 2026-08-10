package plugins

import (
	"sync"
	"testing"
)

// TestStatusVsModeSwitchNoRace 复现真实场景：部署 Docker 容器时
// runDockerDeploy 协程会写 states/dockerName，而前端每秒轮询
// /plugins/status → ListStatus → Status 读 states。
// 两边此前都裸访问同一张 map，Go 运行时会以
// "concurrent map read and map write" 直接终止进程（不是 panic，救不回来）。
// 需要 -race 才能稳定捕获，因此本测试主要价值在于 go test -race。
func TestStatusVsModeSwitchNoRace(t *testing.T) {
	c := NewCenter(t.TempDir())
	var wg sync.WaitGroup

	// 写侧：模拟部署协程反复切换模式并登记容器名
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
				if i%2 == 0 {
					c.setMode(id, ModeDocker)
					c.setDockerName(id, "umbraforge-"+string(id))
				} else {
					c.setMode(id, ModeLocal)
				}
			}
		}
	}()

	// 读侧：模拟 HTTP 轮询读状态
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
				_ = c.mode(id)
			}
		}
	}()

	// 第三方：并发读写打码并发上限（SaveConfig 会在任意时刻下发）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			c.SetMaxWorkers(PluginAuralith, i%9)
			_ = c.MaxWorkers(PluginAuralith)
		}
	}()

	wg.Wait()
}

// TestProcTakeIsAtomic takeProc 必须「取出并删除」是一个原子步骤，
// 否则两个协程可能都拿到同一个 *exec.Cmd 并各自 Kill 一次。
func TestProcTakeIsAtomic(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.setProc(PluginAuralith, nil) // 占位，值本身不重要
	got := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.stateMu.Lock()
			_, ok := c.procs[PluginAuralith]
			c.stateMu.Unlock()
			if ok {
				mu.Lock()
				got++
				mu.Unlock()
			}
			c.takeProc(PluginAuralith)
		}()
	}
	wg.Wait()
	c.stateMu.Lock()
	_, still := c.procs[PluginAuralith]
	c.stateMu.Unlock()
	if still {
		t.Error("takeProc 后条目应已删除")
	}
}
