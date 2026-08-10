package plugins

import (
	"testing"
)

// TestEffectiveModeRespectsDeliberateLocalSwitch effectiveMode 是「自愈」而不是
// 「强制」：它只在容器**确实在跑**时把 states 纠正成 docker。
//
// 这条边界很重要：用户手动把某个引擎切回本机模式时，installLocal 会先
// docker stop 一体容器释放端口；之后 dockerRunningCaptcha 返回空，
// effectiveMode 必须保持 local，不能因为「容器还存在（已停止）」就把用户
// 的选择改回 docker —— 否则本机模式永远切不过去。
func TestEffectiveModeRespectsDeliberateLocalSwitch(t *testing.T) {
	c := NewCenter(t.TempDir())

	// 模拟「曾经是 docker，现在用户切回 local」
	c.setMode(PluginAuralith, ModeDocker)
	c.setMode(PluginAuralith, ModeLocal)

	if !c.DockerAvailable() {
		// 无 Docker：不该有任何探测把它改回 docker
		if got := c.effectiveMode(PluginAuralith); got != ModeLocal {
			t.Errorf("effectiveMode = %q, want %q", got, ModeLocal)
		}
		return
	}
	if c.captchaContainerRunning() {
		t.Skip("本机打码容器正在运行，该分支需要容器停止状态才能验证")
	}
	// 有 Docker 但没有容器在跑 → 必须尊重 local
	if got := c.effectiveMode(PluginAuralith); got != ModeLocal {
		t.Errorf("effectiveMode = %q, want %q —— 容器没在跑时不能把用户的本机选择改回 docker",
			got, ModeLocal)
	}
}

// TestResolveDockerMode 把「该不该纠正成 docker」的判定钉死。
// 纯函数版本，不依赖本机是否有容器在跑，所以能覆盖 effectiveMode 在实机上
// 会被 skip 掉的那两条分支。
func TestResolveDockerMode(t *testing.T) {
	cases := []struct {
		name             string
		stored           Mode
		containerRunning bool
		want             bool
	}{
		{
			// 用户报的 bug：部署过 Docker，重启后 states 丢成 local，
			// 但容器还在跑 —— 必须纠正回 docker。
			name: "重启后 states 丢失但容器在跑", stored: ModeLocal, containerRunning: true, want: true,
		},
		{
			// 反向边界：用户主动切回本机（installLocal 已 stop 容器），
			// 不能被改回 docker，否则本机模式永远切不过去。
			name: "用户主动切本机且容器已停", stored: ModeLocal, containerRunning: false, want: false,
		},
		{
			name: "已是 docker 且容器在跑", stored: ModeDocker, containerRunning: true, want: true,
		},
		{
			// 已标记 docker 但容器停了：模式仍是 docker（Status 会另行报
			// 「容器未运行」），不能悄悄回落成本机，否则用户看不出容器挂了。
			name: "已是 docker 但容器停了", stored: ModeDocker, containerRunning: false, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDockerMode(tc.stored, tc.containerRunning); got != tc.want {
				t.Errorf("resolveDockerMode(%q, %v) = %v, want %v",
					tc.stored, tc.containerRunning, got, tc.want)
			}
		})
	}
}

// TestEffectiveModeIsIdempotent 连续调用结果必须稳定，且不会在 docker 与 local
// 之间来回翻转（前端每 4~5 秒轮询一次，翻转会让状态灯闪烁）。
func TestEffectiveModeIsIdempotent(t *testing.T) {
	c := NewCenter(t.TempDir())
	first := c.effectiveMode(PluginAuralith)
	for i := 0; i < 3; i++ {
		if got := c.effectiveMode(PluginAuralith); got != first {
			t.Fatalf("第 %d 次调用 effectiveMode = %q, 首次 = %q —— 结果必须稳定", i+2, got, first)
		}
	}
}
