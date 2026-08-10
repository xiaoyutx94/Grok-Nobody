package plugins

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDockerBootstrapShellIsValid 用 bash -n 校验拼出来的启动脚本语法。
// 这段脚本只在容器里跑，写错了在宿主上不会报错，只会让容器静默起不来。
func TestDockerBootstrapShellIsValid(t *testing.T) {
	cases := map[string]string{
		"ezsolver": dockerBrowserBootstrap + "pip install -q nodriver fastapi uvicorn httpx 2>/dev/null; " +
			dockerXvfbRun + "python service.py",
		"veloraturn": dockerBrowserBootstrap + "chmod +x /usr/local/bin/veloraturn && " +
			dockerXvfbRun + "/usr/local/bin/veloraturn",
		"auralith": dockerBrowserBootstrap + "chmod +x /usr/local/bin/auralithd && " +
			dockerXvfbRun + "/usr/local/bin/auralithd",
	}
	for name, script := range cases {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s 启动脚本语法错误: %v\n%s\n--- script ---\n%s", name, err, out, script)
		}
	}
}

// TestDockerBootstrapInstallsBrowserAndVirtualDisplay 固定住关键要素。
// 原实现漏了这两样，容器能起但一次也解不出来。
func TestDockerBootstrapInstallsBrowserAndVirtualDisplay(t *testing.T) {
	if !strings.Contains(dockerBrowserBootstrap, "chromium") {
		t.Error("bootstrap 未安装 chromium：容器里没有浏览器，打码必然失败")
	}
	if !strings.Contains(dockerBrowserBootstrap, "xvfb") {
		t.Error("bootstrap 未安装 xvfb：没有虚拟屏，headful 浏览器起不来")
	}
	if !strings.Contains(dockerXvfbRun, "xvfb-run") {
		t.Error("未通过 xvfb-run 启动")
	}
	// 屏幕尺寸不能是 headless 那种 800x600（本身就是特征）
	if strings.Contains(dockerXvfbRun, "800x600") {
		t.Error("虚拟屏用了 800x600，与 headless 特征一致，应使用常见桌面尺寸")
	}
	if !strings.Contains(dockerXvfbRun, "1280x800x24") {
		t.Errorf("虚拟屏尺寸异常: %q", dockerXvfbRun)
	}
	// 幂等：已装则跳过，避免每次重启都重新 apt
	if !strings.Contains(dockerBrowserBootstrap, "if [ ! -x") {
		t.Error("bootstrap 不是幂等的，容器重启会重复 apt")
	}
	// 绝不能引入 headless
	if strings.Contains(dockerBrowserBootstrap, "headless") || strings.Contains(dockerXvfbRun, "headless") {
		t.Error("Docker 模式不应引入 headless")
	}
}

// TestInstallDockerArgsCarryBrowserEnv 检查三个打码器的 docker run 参数。
func TestInstallDockerArgsCarryBrowserEnv(t *testing.T) {
	c := &Center{root: t.TempDir()}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		args := c.dockerRunArgs(id, "umbra-test", 0)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "CHROME_PATH="+dockerChromePath) {
			t.Errorf("%s 未注入 CHROME_PATH: %s", id, joined)
		}
		if !strings.Contains(joined, "--shm-size=1g") {
			t.Errorf("%s 未设置 --shm-size，Chrome 会因共享内存不足崩溃: %s", id, joined)
		}
		if !strings.Contains(joined, "xvfb-run") {
			t.Errorf("%s 未经 xvfb-run 启动: %s", id, joined)
		}
		if !strings.Contains(joined, "google-chrome-stable") && !strings.Contains(joined, "chromium xvfb") {
			t.Errorf("%s 未安装 Chrome(chrome-stable)+xvfb: %s", id, joined)
		}
		// 容器内不应再叠加离屏偏移（Xvfb 已经是虚拟屏）
		if id != PluginAuralith && !strings.Contains(joined, "EZSOLVER_OFFSCREEN=0") {
			t.Errorf("%s 容器内应显式关闭离屏偏移: %s", id, joined)
		}
	}
}
