package plugins

import (
	"testing"
)

// TestCaptchaDeployOrderCoversAllEngines 一键部署必须覆盖全部三个打码引擎、
// 且顺序稳定（先轻后重），否则「3 个一起部署好」的承诺会漏掉引擎，
// 用户切换通道时仍会踩到未部署的端口。
func TestCaptchaDeployOrderCoversAllEngines(t *testing.T) {
	want := map[PluginID]bool{
		PluginEzSolver:   false,
		PluginVeloraTurn: false,
		PluginAuralith:   false,
	}
	for _, id := range captchaDeployOrder {
		if _, ok := want[id]; !ok {
			t.Errorf("captchaDeployOrder 出现非打码引擎 %q", id)
		}
		want[id] = true
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("captchaDeployOrder 缺少引擎 %q——一键部署必须覆盖全部 3 个打码引擎", id)
		}
	}
	if len(captchaDeployOrder) != 3 {
		t.Errorf("captchaDeployOrder 长度 = %d, want 3", len(captchaDeployOrder))
	}
	// 顺序不变式：Auralith（预编译）必须排在 EzSolver（Python venv，最慢）前面
	pos := map[PluginID]int{}
	for i, id := range captchaDeployOrder {
		pos[id] = i
	}
	if pos[PluginAuralith] > pos[PluginEzSolver] {
		t.Errorf("Auralith(%d) 不应排在 EzSolver(%d) 之后：先轻后重", pos[PluginAuralith], pos[PluginEzSolver])
	}
}

// TestDeployAllTaskKeyNotColliding 批量任务 key 不能与单引擎部署 key 冲突，
// 否则前端轮询 docker-task 时会读到错误的任务进度。
func TestDeployAllTaskKeyNotColliding(t *testing.T) {
	for _, id := range captchaDeployOrder {
		if deployTaskKey(id) == taskKeyDeployAll {
			t.Errorf("deployTaskKey(%q) 与批量任务 key %q 冲突", id, taskKeyDeployAll)
		}
	}
}

// TestMarkAllDockerModesSticksForEveryEngine 一体容器就绪后三个引擎都必须进入
// docker 模式。漏掉任何一个，Status() 就会对它走 local 语义：端口上是容器在
// 响应却报 mode=local，用户看到「部署好的打码没被识别」。
func TestMarkAllDockerModesSticksForEveryEngine(t *testing.T) {
	c := NewCenter(t.TempDir())
	for _, id := range captchaDeployOrder {
		if got := c.mode(id); got != ModeLocal {
			t.Fatalf("NewCenter 初始模式 %q = %q, want %q", id, got, ModeLocal)
		}
	}
	c.markAllDockerModes()
	for _, id := range captchaDeployOrder {
		if got := c.mode(id); got != ModeDocker {
			t.Errorf("markAllDockerModes 后 %q = %q, want %q", id, got, ModeDocker)
		}
	}
}

// TestEffectiveModeKeepsDockerOnceMarked effectiveMode 对已标记 docker 的引擎
// 必须直接返回 docker，不再去探容器 —— 这条快路径既是性能保证，也确保
// 「部署完成 → 前端立刻看到 Docker」不依赖 docker CLI 探测成功。
func TestEffectiveModeKeepsDockerOnceMarked(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.setMode(PluginAuralith, ModeDocker)
	if got := c.effectiveMode(PluginAuralith); got != ModeDocker {
		t.Errorf("effectiveMode(auralith) = %q, want %q", got, ModeDocker)
	}
}

// TestEffectiveModeWithoutDockerStaysLocal 没装 Docker 时 effectiveMode 不能
// 把引擎误判成 docker，也不能因为反复探测拖慢 Status()。
func TestEffectiveModeWithoutDockerStaysLocal(t *testing.T) {
	c := NewCenter(t.TempDir())
	if c.DockerAvailable() {
		t.Skip("本机有 Docker，该分支由真实环境覆盖")
	}
	if got := c.effectiveMode(PluginAuralith); got != ModeLocal {
		t.Errorf("effectiveMode(auralith) = %q, want %q（无 Docker 时必须保持 local）", got, ModeLocal)
	}
	if c.captchaContainerRunning() {
		t.Error("无 Docker 时 captchaContainerRunning 必须为 false")
	}
}
