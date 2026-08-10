package plugins

import (
	"runtime"
	"strings"
	"testing"
)

// TestDaemonDownHintIsActionable 守护进程掉线时给的必须是「怎么修」，
// 而不是把 docker CLI 的 "Cannot connect to the Docker daemon" 原文抛给用户。
// macOS 上最常见的成因是 colima 宿主 socket 变僵尸（VM 还在跑、colima list
// 显示 Running，但所有 docker 命令都连不上），提示里必须点名 colima restart。
func TestDaemonDownHintIsActionable(t *testing.T) {
	h := daemonDownHint()
	if h == "" {
		t.Fatal("提示不能为空")
	}
	if runtime.GOOS == "darwin" && !strings.Contains(h, "colima restart") {
		t.Errorf("macOS 提示应给出 colima restart：%s", h)
	}
	if strings.Contains(h, "插件中心完成 Docker 安装") {
		t.Errorf("守护进程掉线不该让用户去装 Docker：%s", h)
	}
}

// TestCapturedImageIsVersioned 固化镜像必须带 bootstrap 版本号，
// 否则 bootstrap 脚本升级后仍会复用旧镜像（可能缺新增依赖）。
func TestCapturedImageIsVersioned(t *testing.T) {
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		ref := capturedImage(id)
		if !strings.Contains(ref, dockerBootstrapVersion) {
			t.Errorf("%s 固化镜像名 %q 未带 bootstrap 版本 %s", id, ref, dockerBootstrapVersion)
		}
		if !strings.Contains(ref, string(id)) {
			t.Errorf("%s 固化镜像名 %q 未区分插件", id, ref)
		}
	}
	// 三个插件的镜像名必须互不相同
	seen := map[string]bool{}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		ref := capturedImage(id)
		if seen[ref] {
			t.Errorf("固化镜像名冲突: %s", ref)
		}
		seen[ref] = true
	}
}

// TestPublicBaseImagePerPlugin EzSolver 是 Python 服务，必须用 python 镜像；
// 另两个是静态 Go 二进制，debian 即可。
func TestPublicBaseImagePerPlugin(t *testing.T) {
	if got := publicBaseImage(PluginEzSolver); !strings.HasPrefix(got, "python:") {
		t.Errorf("EzSolver 基础镜像应为 python，实际 %q", got)
	}
	for _, id := range []PluginID{PluginVeloraTurn, PluginAuralith} {
		if got := publicBaseImage(id); !strings.HasPrefix(got, "debian:") {
			t.Errorf("%s 基础镜像应为 debian，实际 %q", id, got)
		}
	}
}

// TestBaseImageFallsBackToPublic 本机没有固化镜像时必须回落公共基础镜像，
// 否则会去 registry 拉一个不存在的 umbraforge/* 镜像。
func TestBaseImageFallsBackToPublic(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		got := c.baseImage(id)
		if got == capturedImage(id) {
			// 本机恰好已固化过：可接受，跳过
			continue
		}
		if got != publicBaseImage(id) {
			t.Errorf("%s 未固化时应回落 %q，实际 %q", id, publicBaseImage(id), got)
		}
	}
}

// TestDockerRunArgsUseResolvedBaseImage run 参数里的镜像必须来自 baseImage()，
// 不能再硬编码 —— 否则固化镜像永远不会被用上。
func TestDockerRunArgsUseResolvedBaseImage(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		args := c.dockerRunArgs(id, "t", 0)
		want := c.baseImage(id)
		found := false
		for _, a := range args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s run 参数未使用解析出的镜像 %q: %v", id, want, args)
		}
	}
}

// TestWrapDockerErrPassesThroughNil 守护进程正常时不应改写错误（含 nil）。
func TestWrapDockerErrPassesThroughNil(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	if err := c.wrapDockerErr(nil); err != nil {
		t.Errorf("nil 应原样返回，实际 %v", err)
	}
}
