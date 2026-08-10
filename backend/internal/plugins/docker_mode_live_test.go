package plugins

import (
	"testing"
)

// TestStatusReportsDockerForLiveCaptchaContainer 复现并钉死用户报的
// 「部署好的打码没被识别」：
//
// states 是纯内存表，NewCenter 把三个引擎一律初始化成 ModeLocal 且从不落盘。
// 重启 UmbraForge 后，一个明明由 umbraforge-captcha 一体容器提供服务的引擎
// 会被 Status() 报成 mode=local —— UI 上显示「本机运行」，用户以为部署丢了。
//
// 这个测试模拟「刚重启、states 全是 local」的状态：只要机器上真有打码容器
// 在跑，Status() 就必须报 docker。没有容器/没有 Docker 时跳过（CI 环境）。
func TestStatusReportsDockerForLiveCaptchaContainer(t *testing.T) {
	c := NewCenter(t.TempDir())
	if !c.DockerAvailable() {
		t.Skip("本机无可用 Docker，跳过实机校验")
	}
	if !c.captchaContainerRunning() {
		t.Skip("本机没有打码容器在跑，跳过实机校验")
	}

	for _, id := range captchaDeployOrder {
		// 前置条件：模拟重启后的初值，确保测的是「从 local 自愈到 docker」这条路径
		if got := c.mode(id); got != ModeLocal {
			t.Fatalf("前置条件不成立：%q 初始模式 = %q, want %q", id, got, ModeLocal)
		}
	}

	for _, id := range captchaDeployOrder {
		st := c.Status(id)
		if st.Mode != ModeDocker {
			t.Errorf("Status(%q).Mode = %q, want %q —— 容器在跑却报本机模式，"+
				"就是「部署好的打码没被识别」", id, st.Mode, ModeDocker)
		}
		// 自愈必须落到 states 上，否则每次 Status 都要重新探容器
		if got := c.mode(id); got != ModeDocker {
			t.Errorf("Status(%q) 之后 states 未被纠正：mode = %q, want %q", id, got, ModeDocker)
		}
	}
}

// TestAutoStartLocalBundledSkipsWhenContainerRunning 打码容器在跑时绝不能再起
// 本机实例：8192/8193/8194 已被容器映射占着，本机进程只会抢端口失败，
// 或造成「端口有响应但容器状态是停」的假阳性。
func TestAutoStartLocalBundledSkipsWhenContainerRunning(t *testing.T) {
	c := NewCenter(t.TempDir())
	if !c.DockerAvailable() || !c.captchaContainerRunning() {
		t.Skip("需要本机有打码容器在跑才能验证这条短路")
	}
	// 不应启动任何本机进程
	c.AutoStartLocalBundled()
	c.stateMu.Lock()
	n := len(c.procs)
	c.stateMu.Unlock()
	if n != 0 {
		t.Errorf("打码容器在跑时 AutoStartLocalBundled 启动了 %d 个本机进程，应为 0", n)
	}
}
