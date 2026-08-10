//go:build windows

package plugins

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestHealWindowsPrereqsIdempotentWhenReady 就绪机器上 heal 必须是无副作用
// 幂等操作：不弹 UAC、不返回 needReboot。仅在「虚拟机平台 + WSL2 都已就绪」
// 时执行，否则 Skip —— 未就绪的机器上跑会真的去弹 UAC 改系统，不能自动做。
func TestHealWindowsPrereqsIdempotentWhenReady(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	if !vmPlatformFeatureEnabled() || !wsl2Available() {
		t.Skip("本机虚拟化前置未就绪，跳过（避免测试触发 UAC/系统改动）")
	}
	c := NewCenter(t.TempDir())
	task := &PluginTask{Key: "heal-test", Running: true, Stage: "deploy", Message: "x"}
	if needReboot := c.healWindowsPrereqs(task); needReboot {
		t.Errorf("就绪机器 heal 不应要求重启，得到 needReboot=true")
	}
	if task.Stage != "deploy" {
		t.Errorf("就绪机器 heal 不应改变任务阶段，得到 %q", task.Stage)
	}
}

// TestEnsureWindowsPythonNoopWhenInstalled 已装真实 Python 时自动安装函数
// 必须直接复用现有安装（findWindowsPython），不能去下载安装器。
func TestEnsureWindowsPythonNoopWhenInstalled(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	if findWindowsPython() == "" {
		t.Skip("本机未装真实 Python，跳过（避免测试触发下载/安装）")
	}
	p, err := ensureWindowsPython(filepath.Join(t.TempDir(), "py.log"))
	if err != nil {
		t.Fatalf("ensureWindowsPython: %v", err)
	}
	if p == "" {
		t.Error("应返回真实 Python 路径")
	}
}
