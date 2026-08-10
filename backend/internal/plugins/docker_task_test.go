package plugins

import (
	"strings"
	"testing"
	"time"
)

func TestTaskStateMachine(t *testing.T) {
	c := NewCenter("/tmp/umbraforge-test-root")
	if got := len(c.Tasks()); got != 0 {
		t.Fatalf("fresh center should have no tasks, got %d", got)
	}

	// 包内测试：直接登记一个运行中任务（与 EnsureDockerAsync 创建方式一致）
	c.mu.Lock()
	c.tasks["k1"] = &PluginTask{Key: "k1", Running: true, Started: time.Now()}
	c.mu.Unlock()

	// taskStage / taskLog / taskFinish 更新可被 Task() 读取
	c.taskStage("k1", "install", "installing…")
	c.taskLog("k1", "line one")
	c.taskLog("k1", "line two")
	c.taskLog("k1", "")
	if c.Task("k1").Stage != "install" || c.Task("k1").Message != "installing…" {
		t.Fatalf("stage not persisted: %+v", c.Task("k1"))
	}
	if got := c.Task("k1").Log; len(got) != 2 || got[0] != "line one" || got[1] != "line two" {
		t.Fatalf("log not persisted: %v", got)
	}

	c.taskFinish("k1", true, "ready", "ok")
	t1 := c.Task("k1")
	if !t1.Done || !t1.OK || t1.Running || t1.Stage != "ready" {
		t.Fatalf("finish not persisted: %+v", t1)
	}

	// 未知 key 返回空任务
	if got := c.Task("nope"); got.Key != "nope" || got.Done {
		t.Fatalf("unknown key should return empty task: %+v", got)
	}
}

func TestTaskLogCap(t *testing.T) {
	c := NewCenter("/tmp/umbraforge-test-root")
	c.mu.Lock()
	c.tasks["cap"] = &PluginTask{Key: "cap", Running: true}
	c.mu.Unlock()
	c.taskStage("cap", "install", "")
	for i := 0; i < 250; i++ {
		c.taskLog("cap", "x")
	}
	if got := len(c.Task("cap").Log); got > 200 {
		t.Fatalf("log should be capped at 200, got %d", got)
	}
}

func TestDeployDockerAsyncWithoutDocker(t *testing.T) {
	c := NewCenter("/tmp/umbraforge-test-root")
	if c.DockerAvailable() {
		t.Skip("本机已有 Docker，跳过（部署路径由真实环境验证）")
	}
	_, err := c.DeployDockerAsync(InstallRequest{ID: PluginAuralith, Mode: ModeDocker})
	if err == nil {
		t.Fatal("docker 不可用时 DeployDockerAsync 应返回错误")
	}
	// 提示分两种：没装 Docker vs 装了但守护进程连不上（colima 宿主 socket 变僵尸）。
	// 后者不能让用户去「安装 Docker」，所以文案必须给出 restart 指引。
	msg := err.Error()
	if !strings.Contains(msg, "未检测到 Docker") && !strings.Contains(msg, "守护进程已掉线") {
		t.Fatalf("unexpected error: %v", err)
	}
}
