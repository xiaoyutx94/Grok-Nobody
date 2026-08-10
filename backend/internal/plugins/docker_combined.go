package plugins

import (
	"context"
	"fmt"
	"github.com/umbraforge/desktop/internal/procutil"
	"path/filepath"
	"strings"
	"time"
)

// ── 一体容器：三个打码引擎装进同一个容器 ──
//
// 用户实际每次只用一个打码引擎（设置/注册页切换通道只是换端口），
// 所以不必维护三个独立容器：一个容器同时跑 Auralith + VeloraTurn +
// EzSolver，宿主侧映射 8192/8193/8194 三个端口，切换通道 = 切端口，
// 秒级生效、零重部署。
//
// 部署方式完全复用 Auralith 的成熟链路（docker_payload.go）：
//   - 基础镜像统一 python:3.11-slim（EzSolver 需要 python3/pip，
//     debian 系也能 apt 装 Chromium/Xvfb 供 Go 二进制使用）
//   - 二进制/源码经 docker cp 走守护进程 API 送进容器（不依赖宿主目录共享）
//   - 首次启动容器内 apt 装 Chromium+Xvfb（约 3~8 分钟），
//     健康后 commit 成本地镜像，下次部署秒级复用
//
// 旧的单引擎容器（umbraforge-auralith 等）在重建一体容器时自动移除，
// 避免端口冲突和状态混淆。

const (
	// combinedCaptchaContainer 一体容器名。
	combinedCaptchaContainer = "umbraforge-captcha"

	// capturedAllImageRef 一体容器 bootstrap 完成后 commit 的本地镜像。
	capturedAllImageRef = "umbraforge/captcha-all:" + dockerBootstrapVersion

	// combinedBaseImage 一体容器的基础镜像（无本地固化镜像时使用）。
	combinedBaseImageRef = "python:3.11-slim"

	// combinedCaptchaPluginTag 是一体容器在 ContainerInfo.Plugin 里的标记。
	// 它不是某个单一引擎，所以不能返回 auralith/ezsolver 之一。
	combinedCaptchaPluginTag = "captcha-all"
)

// dockerCombinedEntry 一体容器的入口脚本：依次后台拉起三个引擎后 wait。
// 每个引擎在自己的子 shell 里 export 各自的 PORT / 绑定地址 / 并发上限，
// 互不污染；MAX_WORKERS 只在宿主侧注入过（MW_8192 等）时才设置，
// 0 表示不注入（引擎 auto-tune），与单容器部署语义一致。
//
// 参数对齐 amd64 生产打码机（64.83.43.22，实测 solve p50=5.5s / p95=10s /
// 失败率 0.4%）：三个引擎都挂 Turnstile 补丁目录（/opt/patches/<engine>），
// Auralith 开 context 池（Chrome 常驻复用，消冷启动长尾），EzSolver/VeloraTurn
// 配 soft-timeout 与 launch-concurrency。
const dockerCombinedEntry = `set -e; ` + dockerBrowserBootstrap + `
mkdir -p /opt/patches;
for d in auralith veloraturn ezsolver; do [ -d /tmp/patch-$d ] && mv /tmp/patch-$d /opt/patches/$d; done;
pip install -q nodriver fastapi uvicorn httpx 2>/dev/null || true;
chmod +x /usr/local/bin/auralithd /usr/local/bin/veloraturn 2>/dev/null || true;
cd /app;
( export PORT=8192 TS_PROFILE_DIR=/tmp/ezsolver-profiles EZSOLVER_TS_PATCH_DIR=/opt/patches/ezsolver EZSOLVER_SOFT_TIMEOUT=35 EZSOLVER_PROXY_SOFT_TIMEOUT=55 REUSE_BROWSER=0; [ -n "${MW_8192:-}" ] && export MAX_WORKERS="$MW_8192"; exec ` + dockerXvfbRun + `python service.py ) &
( export PORT=8193 BIND_HOST=0.0.0.0 TS_PROFILE_DIR=/tmp/veloraturn-profiles EZSOLVER_TS_PATCH_DIR=/opt/patches/veloraturn TURBOSTILE_LAUNCH_CONCURRENCY=2 TURBOSTILE_WARM_POOL=0; [ -n "${MW_8193:-}" ] && export MAX_WORKERS="$MW_8193"; exec ` + dockerXvfbRun + `/usr/local/bin/veloraturn ) &
( export PORT=8194 AURALITH_BIND_HOST=0.0.0.0 AURALITH_PROFILE_DIR=/tmp/auralith-profiles AURALITH_TS_PATCH_DIR=/opt/patches/auralith AURALITH_POOL_MODE=context AURALITH_CONTEXT_PROCESSES=6 AURALITH_CONTEXTS_PER_PROCESS=2 AURALITH_CONTEXT_IDLE_TTL_SEC=60 AURALITH_CONTEXT_MAX_USES=128 AURALITH_CONTEXT_MAX_AGE_SEC=1800 AURALITH_CONTEXT_CIRCUIT_SEC=15 AURALITH_MINIMAL_DOCUMENT=1 AURALITH_LAUNCH_CONCURRENCY=2 AURALITH_SOFT_TIMEOUT=45 AURALITH_LEAN_MEM=0; [ -n "${MW_8194:-}" ] && export MAX_WORKERS="$MW_8194"; exec ` + dockerXvfbRun + `/usr/local/bin/auralithd ) &
wait`

// combinedBaseImageFor 纯逻辑：有固化镜像用固化镜像（跳过容器内 apt），
// 否则回落公共基础镜像。exists 由调用方注入——生产用 docker image inspect，
// 测试用假函数，避免测试依赖真实守护进程/镜像状态。
func combinedBaseImageForMode(exists func(ref string) bool, allowCaptured bool) string {
	if allowCaptured && exists(capturedAllImageRef) {
		return capturedAllImageRef
	}
	return combinedBaseImageRef
}

func combinedBaseImageFor(exists func(ref string) bool) string {
	return combinedBaseImageForMode(exists, true)
}

// combinedBaseImage never consumes a code-bearing captured image in a
// protected release. Such images are outside the signed payload boundary.
func (c *Center) combinedBaseImage() string {
	return combinedBaseImageForMode(c.imageExists, !protectedRelease())
}

// dockerPayloadsAll 返回一体容器需要 cp 进去的全部文件：
//   - Auralith 二进制 → /usr/local/bin/auralithd
//   - VeloraTurn 二进制 → /usr/local/bin/veloraturn
//   - EzSolver 源码（service.py/solver.py）→ /app
//   - 三个引擎各自的 Turnstile 补丁目录 → /opt/patches/<engine>/
//     （补丁来自 amd64 生产打码机：screenX/Y 随机化 + frame-ready 标记，
//     直接决定挑战页是否被判自动化，实测失败率 14.5% → 0.4%）
func (c *Center) dockerPayloadsAll() ([]dockerPayload, error) {
	arch := c.dockerEngineArch()
	var out []dockerPayload

	for _, id := range []PluginID{PluginAuralith, PluginVeloraTurn} {
		staged, err := c.stageDockerPayload(id, arch)
		if err != nil {
			cleanupDockerPayloads(out)
			return nil, err
		}
		destName := map[PluginID]string{PluginVeloraTurn: "veloraturn", PluginAuralith: "auralithd"}[id]
		out = append(out, dockerPayload{
			Src:     filepath.Join(staged, destName),
			Dest:    "/usr/local/bin/" + destName,
			Cleanup: staged,
		})
	}
	stagedEz, err := c.stageDockerPayload(PluginEzSolver, arch)
	if err != nil {
		cleanupDockerPayloads(out)
		return nil, err
	}
	out = append(out, dockerPayload{Src: stagedEz, Dest: "/app", Cleanup: stagedEz})

	// Turnstile 补丁目录（amd64 生产同款；目录缺失时跳过，不影响部署）。
	// docker cp 无法为多级目标自动建目录（/opt/patches 不存在会直接报错），
	// 先 cp 到单级 /tmp/patch-<engine>，入口脚本再归位到 /opt/patches/<engine>。
	for _, id := range captchaDeployOrder {
		src := filepath.Join(c.root, "plugins", string(id), "turnstilePatch")
		if !fileExists(src) {
			continue
		}
		out = append(out, dockerPayload{Src: src, Dest: "/tmp/patch-" + string(id)})
	}
	return out, nil
}

// dockerRunArgsCombined 组装一体容器的 docker run 参数。
// 三个端口都只发布到 127.0.0.1（免费打码器默认无鉴权，不暴露到局域网）。
// 并发上限按引擎分别注入 MW_8192/8193/8194，入口脚本再映射成各自的 MAX_WORKERS。
func (c *Center) dockerRunArgsCombined(image string) []string {
	args := []string{"run", "-d", "--name", combinedCaptchaContainer,
		"-p", "127.0.0.1:8192:8192",
		"-p", "127.0.0.1:8193:8193",
		"-p", "127.0.0.1:8194:8194",
		"--label", "umbraforge.bootstrap=" + dockerBootstrapVersion,
		// 重启策略：Docker 引擎重启（colima restart / 改 VM 规格 / 开机）后容器要自己回来
		"--restart", "unless-stopped",
		"-e", "CHROME_PATH=" + dockerChromePath,
		"-e", "EZSOLVER_OFFSCREEN=0",
		"--shm-size=1g",
	}
	workerEnv := []struct {
		id  PluginID
		key string
	}{
		{PluginEzSolver, "MW_8192"},
		{PluginVeloraTurn, "MW_8193"},
		{PluginAuralith, "MW_8194"},
	}
	for _, we := range workerEnv {
		if n := c.clampToCapacity(we.id, c.MaxWorkers(we.id)); n > 0 {
			args = append(args, "-e", fmt.Sprintf("%s=%d", we.key, n))
		}
	}
	args = append(args, image, "bash", "-lc", dockerCombinedEntry)
	return args
}

// createCombinedContainer 建一体容器：create → cp 三个引擎的文件 → start。
// 与单引擎的 createContainerWithPayload 同一套模式（docker cp 走守护进程 API）。
func (c *Center) createCombinedContainer(image string, logf func(string)) error {
	log := func(s string) {
		if logf != nil {
			logf(s)
		}
	}
	payloads, err := c.dockerPayloadsAll()
	if err != nil {
		return err
	}
	defer cleanupDockerPayloads(payloads)
	_ = procutil.Command(dockerExecutable(), "rm", "-f", combinedCaptchaContainer).Run()

	args := dockerCreateArgs(c.dockerRunArgsCombined(image))
	log("$ docker " + strings.Join(args, " "))
	if out, err := procutil.Command(dockerExecutable(), args...).CombinedOutput(); err != nil {
		log(strings.TrimSpace(string(out)))
		return fmt.Errorf("创建容器失败: %v / %s", err, trunc(string(out), 300))
	}
	for _, p := range payloads {
		log(fmt.Sprintf("$ docker cp %s %s:%s", filepath.Base(p.Src), combinedCaptchaContainer, p.Dest))
		out, err := procutil.Command(dockerExecutable(), "cp", p.Src, combinedCaptchaContainer+":"+p.Dest).CombinedOutput()
		if err != nil {
			log(strings.TrimSpace(string(out)))
			_ = procutil.Command(dockerExecutable(), "rm", "-f", combinedCaptchaContainer).Run()
			return fmt.Errorf("送入 %s 失败: %v / %s", p.Dest, err, trunc(string(out), 300))
		}
	}
	if out, err := procutil.Command(dockerExecutable(), "start", combinedCaptchaContainer).CombinedOutput(); err != nil {
		log(strings.TrimSpace(string(out)))
		return fmt.Errorf("启动容器失败: %v / %s", err, trunc(string(out), 300))
	}
	return nil
}

// awaitAllCaptchaHealthy 轮询三个打码引擎的 /health 全部就绪。
// 容器内首次要 apt 装 Chromium + Xvfb，放宽到 10 分钟；
// 期间每 10 秒汇报一次各引擎状态与容器日志尾部。
func (c *Center) awaitAllCaptchaHealthy(t *PluginTask) {
	t.Stage = "health"
	t.Message = "等待 3 个打码引擎就绪（容器内首次安装 Chromium/Xvfb，可能需要几分钟）…"
	started := time.Now()
	deadline := started.Add(600 * time.Second)
	last := time.Now()
	ids := captchaDeployOrder
	// repaired 保证端口转发只自愈一次：反复重启容器会把首次 apt 装 Chromium
	// 的进度一并打断，越修越慢。
	repaired := false
	for time.Now().Before(deadline) {
		allOK := true
		for _, id := range ids {
			if !c.Status(id).Healthy {
				allOK = false
				break
			}
		}
		if allOK {
			t.Stage = "ready"
			t.Message = "3 个打码引擎全部就绪（127.0.0.1:8192 / 8193 / 8194），可随时切换通道"
			t.OK = true
			c.taskLog(t.Key, "三个打码引擎健康检查全部通过")
			c.captureImageAll(t)
			return
		}
		if time.Since(last) >= 10*time.Second {
			var parts []string
			for _, id := range ids {
				st := c.Status(id)
				if st.Healthy {
					parts = append(parts, string(id)+"=ok")
				} else {
					parts = append(parts, string(id)+"=wait")
				}
			}
			c.taskLog(t.Key, "健康检查未通过："+strings.Join(parts, " ")+"（容器日志尾部见下）")
			if tail := c.containerLogTail(combinedCaptchaContainer); tail != "" {
				c.taskLog(t.Key, "容器日志: "+tail)
			}
			last = time.Now()
		}
		// 端口转发自愈：容器内健康但宿主连不上 = lima/colima 没建起这几个端口的
		// 转发（实测重建容器后 8193/8194 的 ssh 转发缺失，8192 正常）。
		// 不修的话这里会一直等到 10 分钟超时并报「未就绪」，而三个引擎其实
		// 全是好的 —— 重启一次容器即可让 lima 重新建立转发。
		if !repaired && time.Since(started) >= forwardGrace && c.repairPortForwards(t) {
			repaired = true
		}
		time.Sleep(3 * time.Second)
	}
	tail := c.containerLogTail(combinedCaptchaContainer)
	t.Stage = "failed"
	t.Message = "打码容器在 10 分钟内未就绪。"
	if tail != "" {
		t.Message += " 容器日志尾部：" + trunc(tail, 400)
	}
}

// forwardGrace 是判定「端口转发缺失」前的宽限期。必须长于引擎正常启动时间，
// 否则会把「引擎还在起」误判成「转发坏了」而白重启一次容器。
const forwardGrace = 45 * time.Second

// captchaPorts 是三个打码引擎的固定端口。此前这张表在 center.go 里被抄了三份
// （Status / installLocal / dockerRunArgs），改端口要同时改三处才不出错。
var captchaPorts = map[PluginID]int{
	PluginEzSolver:   8192,
	PluginVeloraTurn: 8193,
	PluginAuralith:   8194,
}

// captchaPort 返回引擎的默认端口（未知引擎返回 0）。
func captchaPort(id PluginID) int { return captchaPorts[id] }

// captchaHealthInContainer 在容器内部探某端口的 /health。
// 容器镜像里不一定有 curl，所以用 python3（基础镜像是 python:3.11-slim，必有）。
func (c *Center) captchaHealthInContainer(port int) bool {
	script := fmt.Sprintf(
		`import urllib.request,sys
try:
    urllib.request.urlopen("http://127.0.0.1:%d/health",timeout=3); sys.exit(0)
except Exception: sys.exit(1)`, port)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return procutil.CommandContext(ctx, dockerExecutable(), "exec", combinedCaptchaContainer,
		"python3", "-c", script).Run() == nil
}

// repairPortForwards 处理「容器内健康、宿主连不上」这一类端口转发缺失。
//
// 实测场景（macOS + colima）：重建一体容器后，lima 只为 8192 建立了 ssh 转发，
// 8193/8194 一个都没建 —— 容器内三个引擎全部 200，宿主侧 lsof 显示这两个端口
// 没有任何监听者。健康轮询是从宿主探的，于是一直等到 10 分钟超时报失败，
// 而引擎其实全好。重启一次容器会让 lima 重新走一遍事件、补齐转发。
//
// 返回是否执行了重启。只在「至少一个端口容器内健康但宿主不通」时动手，
// 避免把「引擎还没起来」误判成转发问题。
func (c *Center) repairPortForwards(t *PluginTask) bool {
	var broken []int
	for _, id := range captchaDeployOrder {
		port := captchaPort(id)
		if c.Status(id).Healthy {
			continue // 宿主侧已通，转发没问题
		}
		if c.captchaHealthInContainer(port) {
			broken = append(broken, port)
		}
	}
	if len(broken) == 0 {
		return false // 引擎自己还没起来，不是转发问题
	}
	c.taskLog(t.Key, fmt.Sprintf(
		"检测到端口转发缺失：容器内 %v 已健康但宿主连不上（lima/colima 未建立转发），"+
			"重启容器以重建转发…", broken))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if out, err := procutil.CommandContext(ctx, dockerExecutable(), "restart",
		combinedCaptchaContainer).CombinedOutput(); err != nil {
		c.taskLog(t.Key, "重启容器失败（继续等待）："+trunc(string(out), 200))
		return true // 已尝试过，不再重复
	}
	c.taskLog(t.Key, "容器已重启，等待端口转发重新建立…")
	return true
}

// captureImageAll 把 bootstrap 完成的一体容器 commit 成本地镜像供下次复用。
// 已存在则跳过。失败不影响本次部署（只是下次还得再 apt 一遍）。
func (c *Center) captureImageAll(t *PluginTask) {
	if protectedRelease() {
		c.taskLog(t.Key, "受保护发布不固化执行容器：避免明文打码核心进入可复用 Docker 镜像")
		return
	}
	if c.imageExists(capturedAllImageRef) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "commit", combinedCaptchaContainer, capturedAllImageRef).CombinedOutput()
	if err != nil {
		c.taskLog(t.Key, "固化一体镜像失败（不影响本次部署，下次仍会重新装 Chromium）："+
			trunc(string(out), 200))
		return
	}
	c.taskLog(t.Key, "已固化一体镜像 "+capturedAllImageRef+"：下次部署可跳过容器内 Chromium 安装")
}

// markAllDockerModes 一体容器就绪后三个引擎都进入 docker 模式，
// 设置/注册页切换通道直接可用，无需再逐个部署。
func (c *Center) markAllDockerModes() {
	for _, id := range captchaDeployOrder {
		c.setMode(id, ModeDocker)
	}
}

// dockerCaptchaName 返回某引擎当前实际由哪个容器承载：
// 一体容器存在就用它，否则回退单引擎容器名（兼容旧部署）。
func (c *Center) dockerCaptchaName(id PluginID) string {
	if c.containerState(combinedCaptchaContainer).Exists {
		return combinedCaptchaContainer
	}
	return c.containerName(id)
}

// effectiveMode 以「实际在跑什么」为准判定模式，而不是只读内存里的 states。
//
// states 是纯内存表且 NewCenter 把三个引擎一律初始化成 ModeLocal，从不落盘 ——
// 于是重启 UmbraForge 后，一个明明由 umbraforge-captcha 一体容器提供服务的引擎
// 会被报成 mode=local。后果不只是 UI 显示错：
//   - 设置/Docker 页把 Docker 部署好的引擎显示成「本机运行」，用户以为部署没被识别；
//   - AutoStartLocalBundled 会以 local 语义去抢 8192/8193/8194；
//   - runDockerDeployInner 的端口清理分支按 local 残留处理，语义全错。
//
// 这里探一次容器：有打码容器在跑就是 docker 模式，并顺手把 states 纠正回来
// （自愈），后续调用无需重复探测。
func (c *Center) effectiveMode(id PluginID) Mode {
	stored := c.mode(id)
	// 快路径：已经是 docker 就不必再探容器。
	if stored == ModeDocker {
		return ModeDocker
	}
	// 只有 docker 可用时才值得探；没装 Docker 时保持 local，避免每次 Status 都超时等待。
	if !c.DockerAvailable() {
		return stored
	}
	if !resolveDockerMode(stored, c.dockerRunningCaptcha(id) != "") {
		return stored
	}
	c.setMode(id, ModeDocker)
	return ModeDocker
}

// resolveDockerMode 判断「该不该把内存里的 local 纠正成 docker」。
// 抽成纯函数是为了能在不停用户容器的前提下单测这条边界：
//
//	containerRunning=true  → 纠正（部署过 Docker、重启后 states 丢了）
//	containerRunning=false → 保持（用户主动切回本机；installLocal 已 stop 容器）
//
// 只看「有没有容器在跑」，不看「容器是否存在」——已停止的容器不能把用户的
// 本机选择改回 docker，否则本机模式永远切不过去。
func resolveDockerMode(stored Mode, containerRunning bool) bool {
	if stored == ModeDocker {
		return true
	}
	return containerRunning
}

// captchaContainerRunning 是否有任何打码容器（一体或旧单引擎）正在运行。
// AutoStartLocalBundled 用它决定「该不该在本机再拉一份」——容器已经占着
// 8192/8193/8194 时再起本机实例只会抢端口并让状态判定出现假阳性。
func (c *Center) captchaContainerRunning() bool {
	if !c.DockerAvailable() {
		return false
	}
	for _, id := range captchaDeployOrder {
		if c.dockerRunningCaptcha(id) != "" {
			return true
		}
	}
	return false
}

// captchaContainerExists 是否有任何打码容器（一体或旧单引擎）存在
// （任意状态：running/stopped/created 都算）。AutoStartLocalBundled 用它
// 决定「该不该在本机再拉一份」的更严格版本：容器存在（即使没在跑）就不
// 启动本地实例——否则本地进程会占住 8192/8193/8194，等用户切回 Docker 时
// docker start 的端口映射绑定失败，出现「设置是 docker 模式、实际打码走
// 本地进程」的假象（容器 running 但端口被本地进程服务）。
func (c *Center) captchaContainerExists() bool {
	if !c.DockerAvailable() {
		return false
	}
	out, err := procutil.Command(dockerExecutable(), "ps", "-a",
		"--filter", "name="+combinedCaptchaContainer,
		"--format", "{{.Names}}").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return true
	}
	for _, id := range captchaDeployOrder {
		out, err := procutil.Command(dockerExecutable(), "ps", "-a",
			"--filter", "name="+c.containerName(id),
			"--format", "{{.Names}}").Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return true
		}
	}
	return false
}

// dockerRunningCaptcha 返回正在运行的打码容器名（一体容器或旧单引擎容器）。
// docker ps 的多个 --filter name= 是 OR 语义，一次调用覆盖两种情况；
// 返回空串 = 没有任何打码容器在跑（端口可能是 local 实例占着的假阳性）。
func (c *Center) dockerRunningCaptcha(id PluginID) string {
	out, _ := procutil.Command(dockerExecutable(), "ps",
		"--filter", "name="+combinedCaptchaContainer,
		"--filter", "name="+c.containerName(id),
		"--format", "{{.Names}}").CombinedOutput()
	return strings.TrimSpace(string(out))
}

// installDockerMode 把某引擎切到 Docker 模式——只判断与切换，不重新部署：
//   - 一体容器已部署 → 直接用（停了就拉起），秒级切换，绝不重装
//   - 旧单引擎容器已部署（老版本遗留）→ 兼容使用
//   - 都没部署 → 明确报错并指路「Docker 管理 → 打码引擎」一键部署
//
// 这是「切换到 docker 打码」的入口：之前前端把 docker_ok=false 一律当未安装
// 触发全自动重装，这里改为先判断部署状态，未部署给可操作提示而不是报错。
func (c *Center) installDockerMode(id PluginID) (PluginStatus, error) {
	if !c.daemonAlive() {
		st := c.Status(id)
		st.Message = daemonDownHint()
		return st, fmt.Errorf("%s", daemonDownHint())
	}
	// 一体容器优先（用户部署形态）
	if c.containerState(combinedCaptchaContainer).Exists {
		// 关键：先清掉端口上的本地残留打码进程，再启动容器。
		// 否则本地实例占着 8192/8193/8194，docker start 后 docker-proxy
		// 绑定失败，容器 running 但端口仍由本地进程服务——注册实际走
		// 的是本地打码，而界面显示 docker 模式。killPortOwner 有
		// isDockerInfraProcess 白名单保护（docker/colima/lima/ssh 不杀）。
		c.killPortOwner(8192)
		c.killPortOwner(8193)
		c.killPortOwner(8194)
		// docker start 对运行中的容器是幂等空操作；停了就拉起
		_ = procutil.Command(dockerExecutable(), "start", combinedCaptchaContainer).Run()
		c.setMode(id, ModeDocker)
		return c.Status(id), nil
	}
	// 老部署兼容：单引擎容器还在 → 直接切
	if c.containerState(c.containerName(id)).Exists {
		c.setMode(id, ModeDocker)
		return c.Status(id), nil
	}
	st := c.Status(id)
	st.Mode = ModeDocker
	st.Message = "该引擎尚未部署到 Docker。请到「Docker 管理 → 打码引擎」点击「一键部署全部」，三个引擎会一起装进同一个容器，之后可随时切换。"
	return st, fmt.Errorf("%s", st.Message)
}
