package plugins

import (
	"testing"
	"time"
)

// TestCaptchaPortsMatchDeployedMapping 端口表是唯一事实来源。
// 这张表此前在 center.go 里被抄了三份，改端口漏一处就会出现
// 「容器映射 8194 而健康检查探 8192」这种查半天的错。
func TestCaptchaPortsMatchDeployedMapping(t *testing.T) {
	want := map[PluginID]int{
		PluginEzSolver:   8192,
		PluginVeloraTurn: 8193,
		PluginAuralith:   8194,
	}
	for id, port := range want {
		if got := captchaPort(id); got != port {
			t.Errorf("captchaPort(%q) = %d, want %d", id, got, port)
		}
	}
	if got := captchaPort(PluginID("nope")); got != 0 {
		t.Errorf("captchaPort(未知引擎) = %d, want 0", got)
	}
	// 一体容器把三个端口全部映射出去，所以三者必须互不相同
	seen := map[int]PluginID{}
	for _, id := range captchaDeployOrder {
		p := captchaPort(id)
		if prev, dup := seen[p]; dup {
			t.Errorf("端口 %d 被 %q 与 %q 同时占用", p, prev, id)
		}
		seen[p] = id
	}
}

// TestForwardGraceLongerThanEngineBoot 宽限期必须明显长于引擎正常启动耗时，
// 否则 repairPortForwards 会把「引擎还在起」当成「转发坏了」，白重启一次容器
// 并打断容器内首次 apt 装 Chromium 的进度。
func TestForwardGraceLongerThanEngineBoot(t *testing.T) {
	// 实测引擎从容器启动到 listening 约 1~3 秒（固化镜像）；
	// 留足余量但不能长到让用户干等，30~120 秒之间是合理区间。
	if forwardGrace < 30*time.Second {
		t.Errorf("forwardGrace = %v，太短：会把正常启动误判成转发故障", forwardGrace)
	}
	if forwardGrace > 120*time.Second {
		t.Errorf("forwardGrace = %v，太长：转发真坏了也要干等这么久", forwardGrace)
	}
}

// TestRepairPortForwardsNoopWhenNoContainer 没有容器时不能误判成需要修转发，
// 更不能去 restart 一个不存在的容器。
func TestRepairPortForwardsNoopWhenNoContainer(t *testing.T) {
	c := NewCenter(t.TempDir())
	if !c.DockerAvailable() {
		t.Skip("无 Docker")
	}
	if c.containerState(combinedCaptchaContainer).Exists {
		t.Skip("本机存在一体容器，该分支需要无容器环境")
	}
	c.mu.Lock()
	c.tasks["t"] = &PluginTask{Key: "t", Running: true}
	c.mu.Unlock()
	if c.repairPortForwards(&PluginTask{Key: "t"}) {
		t.Error("没有容器时 repairPortForwards 不应执行重启")
	}
}
