package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{
		"aarch64": "arm64", "arm64": "arm64", "ARM64": "arm64", "armv8": "arm64",
		"x86_64": "amd64", "amd64": "amd64", "x86-64": "amd64",
	}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
	// 空值回退宿主架构
	if got := normalizeArch(""); got != runtimeGOARCH() {
		t.Errorf("normalizeArch(\"\") = %q, want 宿主 %q", got, runtimeGOARCH())
	}
}

// TestLinuxPluginBinaryIsArchAware 回归：旧实现固定复制 *-linux-amd64。
// 宿主是 Apple Silicon 时 Docker 引擎是 linux/arm64，amd64 的 ELF 在容器里
// 无法执行，xvfb-run 报 "Permission denied"、容器 exit 126。
func TestLinuxPluginBinaryIsArchAware(t *testing.T) {
	for _, id := range []PluginID{PluginAuralith, PluginVeloraTurn} {
		arm := linuxPluginBinary(id, "arm64")
		amd := linuxPluginBinary(id, "amd64")
		if len(arm) == 0 || len(amd) == 0 {
			t.Fatalf("%s 没有返回候选二进制名", id)
		}
		for _, n := range arm {
			if !strings.Contains(n, "arm64") {
				t.Errorf("%s arm64 候选 %q 不含 arm64", id, n)
			}
			if strings.Contains(n, "amd64") {
				t.Errorf("%s arm64 候选 %q 含 amd64（架构写死了）", id, n)
			}
		}
		for _, n := range amd {
			if !strings.Contains(n, "amd64") {
				t.Errorf("%s amd64 候选 %q 不含 amd64", id, n)
			}
		}
	}
}

// TestDockerCreateArgsDropsRunFlags create 不接受 -d，且必须把 run 换成 create。
func TestDockerCreateArgsDropsRunFlags(t *testing.T) {
	got := dockerCreateArgs([]string{"run", "-d", "--name", "x", "-p", "1:2", "img"})
	joined := strings.Join(got, " ")
	if got[0] != "create" {
		t.Errorf("首个参数应为 create，实际 %q", got[0])
	}
	if strings.Contains(joined, " -d") {
		t.Errorf("create 不该带 -d: %s", joined)
	}
	if !strings.Contains(joined, "--name x") || !strings.Contains(joined, "img") {
		t.Errorf("其余参数丢失: %s", joined)
	}
}

// TestDockerRunArgsHaveNoBindMounts 这是 exit 126 的根因回归。
// -v 的源路径在 Docker VM 内解析；宿主目录没共享进 VM 时守护进程会
// 静默创建同名空目录顶上，容器里 /usr/local/bin/auralithd 就成了目录。
// 现在统一用 docker cp 送文件，任何 -v 都不该再出现。
func TestDockerRunArgsHaveNoBindMounts(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		args := c.dockerRunArgs(id, "umbra-test", 0)
		for i, a := range args {
			if a == "-v" || a == "--volume" || strings.HasPrefix(a, "--mount") {
				t.Errorf("%s 仍在用绑定挂载（args[%d]=%q）：挂载源在 Docker VM 里可能不可见，"+
					"会被静默替换成空目录导致 exit 126", id, i, a)
			}
		}
	}
}

// TestDockerRunArgsCarryMaxWorkers Docker 模式过去完全没有 MAX_WORKERS，
// 容器里的引擎按 CPU auto-tune 直接拉满浏览器。
func TestDockerRunArgsCarryMaxWorkers(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		// 未注入时不应出现 MAX_WORKERS（交给引擎自身默认）
		if joined := strings.Join(c.dockerRunArgs(id, "t", 0), " "); strings.Contains(joined, "MAX_WORKERS") {
			t.Errorf("%s 未注入并发时不应写 MAX_WORKERS: %s", id, joined)
		}
		c.SetMaxWorkers(id, 3)
		joined := strings.Join(c.dockerRunArgs(id, "t", 0), " ")
		if !strings.Contains(joined, "MAX_WORKERS=3") {
			t.Errorf("%s docker 模式未注入 MAX_WORKERS=3: %s", id, joined)
		}
	}
}

// TestDockerRunArgsBindAllInsideContainer 容器内必须监听 0.0.0.0。
// veloraturn 默认 BIND_HOST=127.0.0.1、auralithd 默认 AURALITH_BIND_HOST=127.0.0.1，
// 只在容器 loopback 上听时端口映射穿不进去（实测容器内 /proc/net/tcp = 0100007F:2002），
// 健康检查会一直失败直到 10 分钟超时。
func TestDockerRunArgsBindAllInsideContainer(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	want := map[PluginID]string{
		PluginVeloraTurn: "BIND_HOST=0.0.0.0",
		PluginAuralith:   "AURALITH_BIND_HOST=0.0.0.0",
	}
	for id, env := range want {
		if joined := strings.Join(c.dockerRunArgs(id, "t", 0), " "); !strings.Contains(joined, env) {
			t.Errorf("%s 缺 %s，容器内只听 loopback 会导致健康检查永远失败: %s", id, env, joined)
		}
	}
}

// TestDockerRunArgsPublishToLoopbackOnly 免费打码器默认无鉴权，
// 宿主侧只能发布到 127.0.0.1，不能是 0.0.0.0（那样整个局域网都能调用打码）。
func TestDockerRunArgsPublishToLoopbackOnly(t *testing.T) {
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		args := c.dockerRunArgs(id, "t", 0)
		for i, a := range args {
			if a != "-p" || i+1 >= len(args) {
				continue
			}
			if !strings.HasPrefix(args[i+1], "127.0.0.1:") {
				t.Errorf("%s 端口发布 %q 未限定 127.0.0.1，无鉴权打码器会暴露到局域网",
					id, args[i+1])
			}
		}
	}
}

// TestSetMaxWorkersAcceptsZeroAsUnset 旧实现 if n>0 守卫，导致 0（=auto）
// 写不进来也清不掉旧值。
func TestSetMaxWorkersAcceptsZeroAsUnset(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.SetMaxWorkers(PluginAuralith, 5)
	if got := c.MaxWorkers(PluginAuralith); got != 5 {
		t.Fatalf("设置 5 后读到 %d", got)
	}
	c.SetMaxWorkers(PluginAuralith, 0)
	if got := c.MaxWorkers(PluginAuralith); got != 0 {
		t.Errorf("设 0 应清空（交回 auto），实际仍是 %d", got)
	}
}

// TestMaxWorkersNoDeadlockUnderCenterLock 回归：MaxWorkers 会在持有 c.mu 的
// 安装路径里被调用（Install → installLocal → applyWorkerEnv）。
// 若它也锁 c.mu，Go 的不可重入 Mutex 会直接自锁——表现为「插件安装接口一直挂着，
// GET 正常但 POST 永不返回」。所以它必须只锁 workersMu。
func TestMaxWorkersNoDeadlockUnderCenterLock(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.SetMaxWorkers(PluginAuralith, 4)

	done := make(chan int, 1)
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		// 模拟 installLocal 在持锁状态下取并发
		done <- c.MaxWorkers(PluginAuralith)
	}()
	select {
	case got := <-done:
		if got != 4 {
			t.Errorf("持 c.mu 时读到 %d，期望 4", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("持有 c.mu 时调用 MaxWorkers 死锁了（它不能再锁 c.mu）")
	}
}

// TestApplyWorkerEnvUnderCenterLock 同上，覆盖真正被 installLocal 调用的那个函数。
func TestApplyWorkerEnvUnderCenterLock(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.SetMaxWorkers(PluginEzSolver, 2)
	done := make(chan []string, 1)
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		done <- c.applyWorkerEnv(PluginEzSolver, []string{"PORT=8192"})
	}()
	select {
	case env := <-done:
		if !strings.Contains(strings.Join(env, " "), "MAX_WORKERS=2") {
			t.Errorf("未注入 MAX_WORKERS: %v", env)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("持有 c.mu 时调用 applyWorkerEnv 死锁")
	}
}

// TestDockerRunArgsUnderCenterLock installDocker 在持 c.mu 时组装 run 参数，
// 而 dockerRunArgs → dockerWorkerEnv → MaxWorkers 也要取并发。
func TestDockerRunArgsUnderCenterLock(t *testing.T) {
	c := NewCenter(t.TempDir())
	c.SetMaxWorkers(PluginAuralith, 3)
	done := make(chan string, 1)
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		done <- strings.Join(c.dockerRunArgs(PluginAuralith, "t", 0), " ")
	}()
	select {
	case joined := <-done:
		if !strings.Contains(joined, "MAX_WORKERS=3") {
			t.Errorf("未注入 MAX_WORKERS=3: %s", joined)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("持有 c.mu 时调用 dockerRunArgs 死锁")
	}
}

// TestApplyWorkerEnvAllEngines 旧实现只有 cmdAuralithLocal 读 maxWorkers，
// EzSolver / VeloraTurn 的注入值存了从没用过。
func TestApplyWorkerEnvAllEngines(t *testing.T) {
	c := NewCenter(t.TempDir())
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		if env := c.applyWorkerEnv(id, []string{"PORT=1"}); strings.Contains(strings.Join(env, " "), "MAX_WORKERS") {
			t.Errorf("%s 未注入并发时不该写 MAX_WORKERS: %v", id, env)
		}
		c.SetMaxWorkers(id, 2)
		env := strings.Join(c.applyWorkerEnv(id, []string{"PORT=1"}), " ")
		if !strings.Contains(env, "MAX_WORKERS=2") {
			t.Errorf("%s 本机模式未注入 MAX_WORKERS=2: %s", id, env)
		}
		if !strings.Contains(env, "PORT=1") {
			t.Errorf("%s 原有环境变量被覆盖: %s", id, env)
		}
	}
}

// TestDockerPayloadsFailFastOnMissingArch 架构不符时必须在部署前就报错，
// 而不是让容器起来再 exit 126、等满 10 分钟健康轮询。
func TestDockerPayloadsFailFastOnMissingArch(t *testing.T) {
	root := t.TempDir()
	// 只放 amd64，制造「引擎要 arm64 但只有 amd64」的情形
	dir := filepath.Join(root, "plugins", "auralith")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auralithd-linux-amd64"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Center{root: root, maxWorkers: map[PluginID]int{}}
	if _, err := c.stageDockerPayload(PluginAuralith, "arm64"); err == nil {
		t.Error("缺 arm64 二进制时 stageDockerPayload 应报错，而不是静默用 amd64")
	} else if !strings.Contains(err.Error(), "arm64") {
		t.Errorf("错误信息应点明架构，实际: %v", err)
	}
}

// TestStageDockerPayloadArchIsolated 暂存目录必须按架构分开，
// 否则换引擎架构时会因「文件已存在且大小一致」跳过复制，继续用错架构的二进制。
func TestStageDockerPayloadArchIsolated(t *testing.T) {
	if a, b := "auralith-arm64", "auralith-amd64"; a == b {
		t.Fatal("unreachable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "plugins", "auralith")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(dir, "auralithd-linux-"+arch), []byte(arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := &Center{root: root, maxWorkers: map[PluginID]int{}}
	p1, err := c.stageDockerPayload(PluginAuralith, "amd64")
	if err != nil {
		t.Skipf("数据目录不可用: %v", err)
	}
	p2, err := c.stageDockerPayload(PluginAuralith, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("两种架构的暂存目录相同 (%s)，会串架构", p1)
	}
	b2, err := os.ReadFile(filepath.Join(p2, "auralithd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b2) != "arm64" {
		t.Errorf("arm64 暂存目录里的二进制内容是 %q，期望 arm64", b2)
	}
}

func TestCleanupDockerPayloadsRemovesPrivateStaging(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "docker-staging", "ezsolver-amd64-random")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "solver.py"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupDockerPayloads([]dockerPayload{{Src: stage, Dest: "/app", Cleanup: stage}})
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("plaintext Docker staging survived cleanup: %v", err)
	}
}
