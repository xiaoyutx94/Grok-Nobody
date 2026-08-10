package plugins

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyInspectDistinguishesDaemonDown 覆盖 classifyInspect 的核心判定：
// 必须把「守护进程掉线」和「容器真的不存在」分开，否则守护进程一抖动，
// 部署逻辑就会误判成「容器不存在」走 rm -f + 重建，把好容器拆了。
func TestClassifyInspectDistinguishesDaemonDown(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		err         error
		daemonAlive bool
		want        containerState
	}{
		{
			name: "运行中且带 bootstrap 版本",
			out:  "true|v4",
			err:  nil,
			want: containerState{Exists: true, Running: true, Bootstrap: "v4"},
		},
		{
			name: "存在但已停止",
			out:  "false|v4",
			err:  nil,
			want: containerState{Exists: true, Running: false, Bootstrap: "v4"},
		},
		{
			// 回归点：容器真不存在时必须允许新建，不能被误判为掉线而拒绝重建。
			name: "容器真的不存在",
			out:  "Error: No such object: umbraforge-auralith",
			err:  errors.New("exit status 1"),
			want: containerState{Exists: false, Running: false, DaemonDown: false},
		},
		{
			// 回归点：守护进程掉线时不能被当成「容器不存在」而触发 rm -f 重建，
			// 那会把一个可能完好的容器拆掉。
			name:        "守护进程掉线",
			out:         "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
			err:         errors.New("exit status 1"),
			daemonAlive: false,
			want:        containerState{Exists: false, Running: false, DaemonDown: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInspect(tc.out, tc.err, tc.daemonAlive)
			if got.Exists != tc.want.Exists {
				t.Errorf("Exists = %v, want %v", got.Exists, tc.want.Exists)
			}
			if got.Running != tc.want.Running {
				t.Errorf("Running = %v, want %v", got.Running, tc.want.Running)
			}
			if got.Bootstrap != tc.want.Bootstrap {
				t.Errorf("Bootstrap = %q, want %q", got.Bootstrap, tc.want.Bootstrap)
			}
			if got.DaemonDown != tc.want.DaemonDown {
				t.Errorf("DaemonDown = %v, want %v", got.DaemonDown, tc.want.DaemonDown)
			}
		})
	}
}

// TestIsDockerInfraProcessProtectsPortForwarders Docker/lima/colima 的运行时
// 和端口转发进程必须被识别为「不能杀」，否则 killPortOwner 会连带打掉同一
// lima ssh 主进程上其它插件的端口转发，把「切换一个插件」放大成
// 「所有容器都不可用」。同时反向验证：真正该让出端口的本地打码进程
// （python3/node/auralithd 等）不能被这个白名单误挡，否则端口永远让不出来。
func TestIsDockerInfraProcessProtectsPortForwarders(t *testing.T) {
	mustProtect := []string{
		"docker", "dockerd", "colima", "limactl", "ssh", "vpnkit",
		"socket_vmnet", "com.docker.backend",
		"/usr/bin/docker", // 带路径的写法也要识别
	}
	for _, comm := range mustProtect {
		if !isDockerInfraProcess(comm) {
			t.Errorf("isDockerInfraProcess(%q) = false，应为 true（Docker 基础设施进程不能被杀）", comm)
		}
	}

	mustNotProtect := []string{"python3", "node", "auralithd", "veloraturn", "ezsolver"}
	for _, comm := range mustNotProtect {
		if isDockerInfraProcess(comm) {
			t.Errorf("isDockerInfraProcess(%q) = true，应为 false（真正该让出端口的本地实例不该被白名单挡住）", comm)
		}
	}
}

// TestDockerRunArgsWithImagePinsImage 部署路径的镜像必须是调用方传入的那个，
// 不能在 dockerRunArgsWithImage 内部再自行解析一次 baseImage()——否则部署过程中
// 守护进程一抖动，两次解析结果可能不一致，日志说复用了固化镜像而实际
// 用的是公共基础镜像，白跑一次 3~8 分钟的容器内 apt。
func TestDockerRunArgsWithImagePinsImage(t *testing.T) {
	const pinned = "umbraforge/pinned:test"
	c := &Center{root: t.TempDir(), maxWorkers: map[PluginID]int{}}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		args := c.dockerRunArgsWithImage(id, "t", 0, pinned)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, pinned) {
			t.Errorf("%s 参数未出现钉死的镜像 %q: %v", id, pinned, args)
		}
		if strings.Contains(joined, "debian:bookworm-slim") {
			t.Errorf("%s 参数不该再出现自行解析的 debian:bookworm-slim: %v", id, args)
		}
		if strings.Contains(joined, "python:3.11-slim") {
			t.Errorf("%s 参数不该再出现自行解析的 python:3.11-slim: %v", id, args)
		}
	}
}
