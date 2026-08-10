package plugins

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCombinedContainerNameIsManaged 一体容器必须被识别为 UmbraForge 自己的
// 托管容器（Docker 管理页才能对它启停/删除），不能落到「其它项目」分区。
func TestCombinedContainerNameIsManaged(t *testing.T) {
	if !isManagedContainer(combinedCaptchaContainer) {
		t.Errorf("isManagedContainer(%q) = false，一体容器必须是托管容器", combinedCaptchaContainer)
	}
}

// TestCombinedRunArgsPublishesThreePorts 一体容器必须映射 8192/8193/8194
// 三个端口（只发布到 127.0.0.1），缺一个端口 = 对应引擎切换后连不上。
func TestCombinedRunArgsPublishesThreePorts(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	args := c.dockerRunArgsCombined("python:3.11-slim")
	joined := strings.Join(args, " ")
	for _, port := range []int{8192, 8193, 8194} {
		want := "127.0.0.1:" + strconv.Itoa(port) + ":" + strconv.Itoa(port)
		if !strings.Contains(joined, want) {
			t.Errorf("一体容器缺少端口映射 %s: %v", want, args)
		}
	}
	// 一体容器不应出现旧的单引擎容器名
	for _, id := range captchaDeployOrder {
		if strings.Contains(joined, "--name "+c.containerName(id)) {
			t.Errorf("一体容器参数里不应再出现单引擎容器名 %s", c.containerName(id))
		}
	}
}

// TestCombinedEntryStartsAllEngines 一体容器入口脚本必须同时拉起三个引擎：
// EzSolver 的 python service.py、VeloraTurn 与 Auralith 的二进制；
// 且各自绑定 0.0.0.0（否则端口映射穿不进容器）。
func TestCombinedEntryStartsAllEngines(t *testing.T) {
	entry := dockerCombinedEntry
	for _, want := range []string{
		"python service.py",
		"/usr/local/bin/veloraturn",
		"/usr/local/bin/auralithd",
		"PORT=8192", "PORT=8193", "PORT=8194",
		"BIND_HOST=0.0.0.0", "AURALITH_BIND_HOST=0.0.0.0",
		// amd64 生产对齐：Turnstile 补丁目录 + context 池 + 超时/并发参数
		"EZSOLVER_TS_PATCH_DIR=/opt/patches/ezsolver",
		"EZSOLVER_SOFT_TIMEOUT=35", "EZSOLVER_PROXY_SOFT_TIMEOUT=55",
		"EZSOLVER_TS_PATCH_DIR=/opt/patches/veloraturn",
		"TURBOSTILE_LAUNCH_CONCURRENCY=2", "TURBOSTILE_WARM_POOL=0",
		"AURALITH_TS_PATCH_DIR=/opt/patches/auralith",
		"AURALITH_POOL_MODE=context",
		"AURALITH_CONTEXT_PROCESSES=6", "AURALITH_CONTEXTS_PER_PROCESS=2",
		"AURALITH_CONTEXT_IDLE_TTL_SEC=60", "AURALITH_CONTEXT_MAX_USES=128",
		"AURALITH_MINIMAL_DOCUMENT=1", "AURALITH_LAUNCH_CONCURRENCY=2",
		"AURALITH_SOFT_TIMEOUT=45", "AURALITH_LEAN_MEM=0",
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("一体容器入口脚本缺少 %q", want)
		}
	}
}

// hasEnvArg 检查 args 里是否有 -e KEY=VALUE。
func hasEnvArg(args []string, keyVal string) bool {
	for i, a := range args {
		if a == "-e" && i+1 < len(args) && args[i+1] == keyVal {
			return true
		}
	}
	return false
}

// TestCombinedWorkerEnvMappedPerEngine 并发上限必须按引擎分别注入
// （MW_8192/MW_8193/MW_8194），注入后入口脚本会映射成各自的 MAX_WORKERS。
func TestCombinedWorkerEnvMappedPerEngine(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{
		PluginEzSolver:   6,
		PluginVeloraTurn: 4,
		PluginAuralith:   8,
	}}
	args := c.dockerRunArgsCombined("python:3.11-slim")
	if !hasEnvArg(args, "MW_8192=6") {
		t.Errorf("EzSolver 并发 6 未注入 MW_8192: %v", args)
	}
	if !hasEnvArg(args, "MW_8193=4") {
		t.Errorf("VeloraTurn 并发 4 未注入 MW_8193: %v", args)
	}
	if !hasEnvArg(args, "MW_8194=8") {
		t.Errorf("Auralith 并发 8 未注入 MW_8194: %v", args)
	}
	// 未设置并发（0）时不得注入任何 MW_*（引擎 auto-tune，与单容器语义一致）
	c2 := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	args2 := c2.dockerRunArgsCombined("python:3.11-slim")
	for _, a := range args2 {
		if a == "-e" {
			continue
		}
		if strings.HasPrefix(a, "MW_") {
			t.Errorf("未设置并发时不应注入 %q: %v", a, args2)
		}
	}
}

// TestCombinedBaseImagePreferCaptured 有固化镜像时优先复用（跳过容器内 apt），
// 否则回落 python:3.11-slim。exists 注入假函数，不依赖真实 Docker 状态
// （本机实机部署过一体容器后固化镜像真实存在，直接查 docker 会让测试抖动）。
func TestCombinedBaseImagePreferCaptured(t *testing.T) {
	// 无固化镜像 → 公共基础镜像
	if got := combinedBaseImageFor(func(ref string) bool { return false }); got != combinedBaseImageRef {
		t.Errorf("无固化镜像时 combinedBaseImageFor = %q, want %q", got, combinedBaseImageRef)
	}
	// 有固化镜像 → 复用固化镜像（跳过容器内 apt）
	if got := combinedBaseImageFor(func(ref string) bool { return ref == capturedAllImageRef }); got != capturedAllImageRef {
		t.Errorf("有固化镜像时 combinedBaseImageFor = %q, want %q", got, capturedAllImageRef)
	}
}

// TestCombinedPayloadsCoverAllEngines 一体容器的 payload 必须覆盖
// 三个引擎的二进制/源码与目标路径，漏一个 = 该引擎在容器里起不来。
func TestCombinedPayloadsCoverAllEngines(t *testing.T) {
	// 用假文件替代真实二进制，让 stageDockerPayload 的复制能通过。
	// dockerEngineArch 探测失败会回退 runtime.GOARCH，两种架构都写，
	// 保证本机与 CI 上都能过。
	c := &Center{root: t.TempDir()}
	writeFake := func(path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		writeFake(c.root + "/plugins/auralith/auralithd-linux-" + arch)
		writeFake(c.root + "/plugins/veloraturn/veloraturn-linux-" + arch)
		writeFake(c.root + "/plugins/veloraturn/veloraturn-go-linux-" + arch)
	}
	writeFake(c.root + "/plugins/ezsolver/service.py")
	writeFake(c.root + "/plugins/ezsolver/solver.py")
	// Turnstile 补丁（amd64 生产同款目录结构）
	for _, id := range []string{"auralith", "veloraturn", "ezsolver"} {
		writeFake(c.root + "/plugins/" + id + "/turnstilePatch/script.js")
		writeFake(c.root + "/plugins/" + id + "/turnstilePatch/manifest.json")
	}

	// dockerEngineArch 依赖 docker 命令；直接注入预期架构验证 payload 逻辑
	payloads, err := c.dockerPayloadsAll()
	if err != nil {
		t.Fatalf("dockerPayloadsAll: %v", err)
	}
	dests := map[string]bool{}
	for _, p := range payloads {
		dests[p.Dest] = true
	}
	for _, want := range []string{
		"/usr/local/bin/auralithd", "/usr/local/bin/veloraturn", "/app",
		"/tmp/patch-auralith", "/tmp/patch-veloraturn", "/tmp/patch-ezsolver",
	} {
		if !dests[want] {
			t.Errorf("一体容器 payload 缺少目标 %q（实际: %v）", want, dests)
		}
	}
}

func TestCombinedBaseImageProtectedModeIgnoresCapturedCodeImage(t *testing.T) {
	exists := func(ref string) bool { return ref == capturedAllImageRef }
	if got := combinedBaseImageForMode(exists, false); got != combinedBaseImageRef {
		t.Fatalf("protected mode selected code-bearing captured image %q", got)
	}
	if got := combinedBaseImageForMode(exists, true); got != capturedAllImageRef {
		t.Fatalf("development mode did not preserve captured image optimization: %q", got)
	}
}
