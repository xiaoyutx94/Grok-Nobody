package plugins

import (
	"context"
	"fmt"
	"github.com/umbraforge/desktop/internal/procutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Docker 模式下把插件文件送进容器的方式，以及「送哪个架构的二进制」。
//
// 原实现用 -v 绑定挂载宿主目录，踩了两个必然失败的坑：
//
//  1. 挂载源不可见 → 容器里是个空目录。Docker 守护进程跑在 VM 里
//     （colima / Docker Desktop），-v 的源路径是在 *VM 内* 解析的。
//     colima 默认只共享 ~ 或用户显式配置的目录；本项目在 /Volumes/Tools+ 下，
//     而挂载源被复制到 ~/Library/Application Support/UmbraForge/docker-mounts/。
//     若该路径没被共享进 VM，守护进程找不到它，就会**静默创建一个空目录**顶上，
//     于是容器里 /usr/local/bin/auralithd 是个目录，报
//     "xvfb-run: /usr/local/bin/auralithd: Permission denied"（exit 126）。
//
//  2. 架构不匹配。宿主是 darwin/arm64，Linux 容器引擎就是 linux/arm64，
//     但挂载源固定复制 *-linux-amd64，ELF 架构不对同样起不来。
//
// 正解：用 docker cp 走守护进程 API 送文件——不依赖任何宿主目录共享，
// 且二进制按**引擎架构**（docker info 的 Architecture，不是宿主 GOARCH）挑选。

// dockerPayload 描述一个要 docker cp 进容器的文件/目录。
type dockerPayload struct {
	Src     string // 宿主路径
	Dest    string // 容器内路径
	Cleanup string // docker cp 完成后删除的私有暂存目录；空表示不可删除
}

func cleanupDockerPayloads(payloads []dockerPayload) {
	seen := map[string]struct{}{}
	for _, payload := range payloads {
		if strings.TrimSpace(payload.Cleanup) == "" {
			continue
		}
		clean := filepath.Clean(payload.Cleanup)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		_ = os.RemoveAll(clean)
	}
}

// runtimeGOARCH 宿主架构（探测引擎架构失败时的回退值）。
func runtimeGOARCH() string { return runtime.GOARCH }

// daemonAlive 快速判断守护进程是否真的能连上。
// 专治 colima 的「宿主 socket 变僵尸」故障：VM 还在跑、colima list 显示
// Running、colima ssh 也通，但宿主侧转发 socket 已死，所有 docker 命令报
// "Cannot connect to the Docker daemon ... Is the docker daemon running?"。
// 部署中途遇到这种情况时要给出可操作的提示，而不是把 CLI 原文抛给用户。
func (c *Center) daemonAlive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return procutil.CommandContext(ctx, dockerExecutable(), "version",
		"--format", "{{.Server.Version}}").Run() == nil
}

// daemonDownHint 守护进程掉线时的可操作提示。
func daemonDownHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "Docker 守护进程已掉线（常见于 colima 宿主 socket 变僵尸：" +
			"colima list 仍显示 Running 但所有 docker 命令都连不上）。" +
			"请执行 `colima restart`（或重启 Docker Desktop）后重试部署。"
	case "linux":
		return "Docker 守护进程已掉线。请执行 `sudo systemctl restart docker` 后重试部署。"
	default:
		return "Docker 守护进程已掉线，请重启 Docker 后重试部署。"
	}
}

// wrapDockerErr 若守护进程已掉线，把原始 CLI 错误替换成可操作提示。
func (c *Center) wrapDockerErr(err error) error {
	if err == nil {
		return nil
	}
	if !c.daemonAlive() {
		return fmt.Errorf("%s（原始错误：%v）", daemonDownHint(), err)
	}
	return err
}

// capturedImage 是首次 bootstrap 成功后 commit 出来的本地镜像名。
// 复用它可跳过容器内那 3~8 分钟的 apt 安装 Chromium：
// 既让重新部署变成秒级，也大幅缩短「长时间 apt 期间守护进程掉线」的暴露窗口。
func capturedImage(id PluginID) string {
	return "umbraforge/captcha-" + string(id) + ":" + dockerBootstrapVersion
}

// imageExists 本地是否已有该镜像。
func (c *Center) imageExists(ref string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return procutil.CommandContext(ctx, dockerExecutable(), "image", "inspect", ref).Run() == nil
}

// containerState 是部署决策需要的容器快照。
// 关键是把「守护进程掉线」和「容器不存在」分开：旧实现 inspect 一失败就
// 当成没有可复用容器，于是走 rm -f + 重建——守护进程抖一下就把已经装好
// Chromium 的好容器拆了。
type containerState struct {
	Exists     bool
	Running    bool
	Bootstrap  string
	DaemonDown bool
}

// classifyInspect 把 docker inspect 的结果归类。抽成纯函数是为了能在没有
// 守护进程的机器上单测这个「掉线 vs 不存在」的判定。
func classifyInspect(out string, err error, daemonAlive bool) containerState {
	if err == nil {
		fields := strings.SplitN(strings.TrimSpace(out), "|", 2)
		st := containerState{Exists: true}
		if len(fields) == 2 {
			st.Running = strings.TrimSpace(fields[0]) == "true"
			st.Bootstrap = strings.TrimSpace(fields[1])
		}
		return st
	}
	// "No such object" 是容器真的不存在，可以放心新建。
	if strings.Contains(strings.ToLower(out), "no such object") {
		return containerState{}
	}
	// 其余失败原因未知，守护进程也连不上时必须当成「不知道」，
	// 让调用方停手而不是重建。
	if !daemonAlive {
		return containerState{DaemonDown: true}
	}
	return containerState{}
}

// containerState 探一次容器现状（存在/在跑/bootstrap 版本/守护进程是否掉线）。
func (c *Center) containerState(name string) containerState {
	out, err := procutil.Command(dockerExecutable(), "inspect", "--format",
		"{{.State.Running}}|{{index .Config.Labels \"umbraforge.bootstrap\"}}", name).CombinedOutput()
	if err == nil {
		return classifyInspect(string(out), nil, true)
	}
	return classifyInspect(string(out), err, c.daemonAlive())
}

// isDockerInfraProcess 判断某进程名是否属于 Docker/lima/colima 的端口转发或运行时基础设施。
// 容器 -p 映射在宿主侧由它们监听，误杀会连带打掉同一 lima ssh 主进程上
// **所有**插件的端口转发（表现为 colima list 仍显示 Running，但 docker
// 命令全部连不上），把「切换一个插件」放大成「所有容器都不可用」。
func isDockerInfraProcess(comm string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(comm)))
	if base == "" {
		return false
	}
	for _, infra := range []string{
		"docker", "dockerd", "containerd", "colima", "limactl", "lima",
		"ssh", "sshd", "vpnkit", "socket_vmnet", "qemu", "com.docker",
	} {
		if base == infra || strings.HasPrefix(base, infra) {
			return true
		}
	}
	return false
}

// dockerEngineArch 返回 Docker 引擎（而非宿主）的 GOARCH 风格架构。
// 容器跑在 Linux VM 里，架构由引擎决定：Apple Silicon + colima = arm64。
// 探测失败时回退宿主 GOARCH（单机同构场景下等价）。
func (c *Center) dockerEngineArch() string {
	out, err := procutil.Command(dockerExecutable(), "info", "--format", "{{.Architecture}}").Output()
	if err != nil {
		return runtimeGOARCH()
	}
	return normalizeArch(strings.TrimSpace(string(out)))
}

// normalizeArch 把 uname/docker 风格架构名归一到 GOARCH 命名。
func normalizeArch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "aarch64", "arm64", "arm64/v8", "armv8", "armv8l":
		return "arm64"
	case "x86_64", "amd64", "x86-64":
		return "amd64"
	case "":
		return runtimeGOARCH()
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// linuxPluginBinary 返回某插件在指定架构下的 Linux 二进制候选名（按优先级）。
// 兼容仓库里的两种历史命名：<name>-linux-<arch> 与旧的 <name>-go-linux-amd64。
func linuxPluginBinary(id PluginID, arch string) []string {
	switch id {
	case PluginAuralith:
		return []string{"auralithd-linux-" + arch}
	case PluginVeloraTurn:
		return []string{
			"veloraturn-linux-" + arch,
			"veloraturn-go-linux-" + arch,
		}
	}
	return nil
}

// dockerPayloads 返回部署该插件需要 cp 进容器的文件。
// 二进制按引擎架构挑选；架构不匹配直接报错，而不是让容器起来再 126。
func (c *Center) dockerPayloads(id PluginID) ([]dockerPayload, error) {
	arch := c.dockerEngineArch()
	switch id {
	case PluginEzSolver:
		// Python 源码与架构无关；staged 目录 cp 成容器里的 /app
		// （docker cp 目录到不存在的目标会创建该目录并复制内容）。
		staged, err := c.stageDockerPayload(id, arch)
		if err != nil {
			return nil, err
		}
		return []dockerPayload{{Src: staged, Dest: "/app", Cleanup: staged}}, nil

	case PluginVeloraTurn, PluginAuralith:
		destName := map[PluginID]string{PluginVeloraTurn: "veloraturn", PluginAuralith: "auralithd"}[id]
		staged, err := c.stageDockerPayload(id, arch)
		if err != nil {
			return nil, err
		}
		return []dockerPayload{{
			Src:     filepath.Join(staged, destName),
			Dest:    "/usr/local/bin/" + destName,
			Cleanup: staged,
		}}, nil
	}
	return nil, fmt.Errorf("unknown plugin %s", id)
}

// dockerCreateArgs 把 dockerRunArgs 的 "run -d" 换成 "create"。
// 先 create 再 cp 再 start：文件经守护进程 API 落进容器可写层，
// 绕开宿主目录共享，也不再需要 :ro 挂载（chmod +x 能直接生效）。
func dockerCreateArgs(runArgs []string) []string {
	out := make([]string, 0, len(runArgs))
	replaced := false
	for _, a := range runArgs {
		if !replaced && a == "run" {
			out = append(out, "create")
			replaced = true
			continue
		}
		if replaced && a == "-d" {
			continue // create 不需要 -d
		}
		out = append(out, a)
	}
	return out
}
