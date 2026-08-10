package plugins

import (
	"strings"
	"testing"
)

// TestLinuxPluginBinaryIsAlwaysLinux 打码容器是 **Linux** 容器，
// 所以不管宿主是 Windows / macOS / Linux，送进容器的二进制永远是 linux 版。
//
// 这条不变式看着显然，但打分发包时极容易踩：Windows 包里出现
// auralithd-linux-amd64 会让人觉得「Windows 用不到，排掉省体积」——
// 一排掉，Windows 用户点「一键部署全部」就报
// 「找不到 linux/amd64 二进制」。我就这么干过一次。
func TestLinuxPluginBinaryIsAlwaysLinux(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, id := range []PluginID{PluginAuralith, PluginVeloraTurn} {
			names := linuxPluginBinary(id, arch)
			if len(names) == 0 {
				t.Fatalf("linuxPluginBinary(%q, %q) 返回空", id, arch)
			}
			for _, n := range names {
				if !strings.Contains(n, "-linux-") {
					t.Errorf("linuxPluginBinary(%q, %q) = %q，必须是 linux 二进制（容器是 Linux）",
						id, arch, n)
				}
				if !strings.HasSuffix(n, arch) {
					t.Errorf("linuxPluginBinary(%q, %q) = %q，架构后缀不符", id, arch, n)
				}
				// 绝不能出现宿主平台的产物
				for _, bad := range []string{"windows", ".exe", "darwin"} {
					if strings.Contains(n, bad) {
						t.Errorf("linuxPluginBinary(%q, %q) = %q，混入了宿主平台产物 %q",
							id, arch, n, bad)
					}
				}
			}
		}
	}
	// EzSolver 是 Python 源码，没有平台二进制
	if got := linuxPluginBinary(PluginEzSolver, "amd64"); got != nil {
		t.Errorf("EzSolver 不应有平台二进制，得到 %v", got)
	}
}

// TestDockerRunArgsCombinedHasNoHostPaths 一体容器的 run 参数里不能出现宿主路径。
//
// Windows 上宿主路径是 C:\... 形式，一旦进了 docker run 的 -v 挂载或环境变量，
// 会因为盘符冒号被 Docker 解析成 container:path 而出错，或在 Linux VM 里指向
// 不存在的目录（Docker 会静默建空目录，表现为「文件送进去了但容器里是空的」）。
// 现在的设计是全部走 docker cp，所以这里钉住「run 参数不含宿主路径」。
func TestDockerRunArgsCombinedHasNoHostPaths(t *testing.T) {
	c := &Center{root: "/tmp/uf-test-root", maxWorkers: map[PluginID]int{}}
	args := c.dockerRunArgsCombined("umbraforge/captcha-all:test")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "-v ") || strings.Contains(joined, "--volume") {
		t.Errorf("run 参数出现挂载（应全部走 docker cp）：%s", joined)
	}
	if strings.Contains(joined, c.root) {
		t.Errorf("run 参数泄漏了宿主路径 %q：%s", c.root, joined)
	}
	// 三个端口都必须只发布到回环：免费打码器默认无鉴权
	for _, p := range []string{"127.0.0.1:8192:8192", "127.0.0.1:8193:8193", "127.0.0.1:8194:8194"} {
		if !strings.Contains(joined, p) {
			t.Errorf("缺少端口映射 %s：%s", p, joined)
		}
	}
	if strings.Contains(joined, "-p 0.0.0.0") || strings.Contains(joined, "-p 8192:") {
		t.Errorf("端口不能暴露到非回环地址：%s", joined)
	}
}
