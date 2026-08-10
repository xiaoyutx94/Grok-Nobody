package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/umbraforge/desktop/internal/procutil"
	"strings"
	"time"
)

// Docker 容器 / 镜像的管理操作，供「Docker 管理」页面调用。
//
// 只暴露必要的动作（启停、重启、删除、清理镜像），并对 UmbraForge 自己创建的
// 对象做标记（Managed），让前端能把「自己的」和「用户其它项目的」分开显示 ——
// 免得误删了用户的 postgres/redis。

// ContainerInfo 是容器的一行摘要。
type ContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`  // running | exited | created …
	Status  string `json:"status"` // "Up 3 minutes" 之类的人类描述
	Ports   string `json:"ports"`
	Managed bool   `json:"managed"` // 是否由 UmbraForge 创建
	Plugin  string `json:"plugin,omitempty"`
}

// ImageInfo 是镜像的一行摘要。
type ImageInfo struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Tag     string `json:"tag"`
	Size    string `json:"size"`
	Managed bool   `json:"managed"`
}

// managedContainerPrefixes 是 UmbraForge 自己创建的容器名前缀。
var managedContainerPrefixes = []string{"umbraforge-", "umbra-warp", "umbra-test"}

// pluginOfContainer 反查容器属于哪个打码插件（不是则返回空）。
func pluginOfContainer(name string) string {
	name = strings.TrimPrefix(name, "/")
	// 一体容器同时承载三个引擎，没有单一 plugin id —— 返回专用标记，
	// 让前端能把它标成「打码 · 三合一」而不是掉进「其它项目」分支。
	if name == combinedCaptchaContainer {
		return combinedCaptchaPluginTag
	}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		if name == "umbraforge-"+string(id) {
			return string(id)
		}
	}
	return ""
}

func isManagedContainer(name string) bool {
	name = strings.TrimPrefix(name, "/")
	for _, p := range managedContainerPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ListContainers 列出所有容器（含已停止的）。
func (c *Center) ListContainers() ([]ContainerInfo, error) {
	if !c.daemonAlive() {
		return nil, fmt.Errorf("%s", daemonDownHint())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "ps", "-a",
		"--format", "{{json .}}").Output()
	if err != nil {
		return nil, c.wrapDockerErr(err)
	}
	var list []ContainerInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r struct {
			ID     string `json:"ID"`
			Names  string `json:"Names"`
			Image  string `json:"Image"`
			State  string `json:"State"`
			Status string `json:"Status"`
			Ports  string `json:"Ports"`
		}
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		name := strings.TrimPrefix(r.Names, "/")
		list = append(list, ContainerInfo{
			ID: r.ID, Name: name, Image: r.Image,
			State: r.State, Status: r.Status, Ports: r.Ports,
			Managed: isManagedContainer(name),
			Plugin:  pluginOfContainer(name),
		})
	}
	return list, nil
}

// ListImages 列出本地镜像。
func (c *Center) ListImages() ([]ImageInfo, error) {
	if !c.daemonAlive() {
		return nil, fmt.Errorf("%s", daemonDownHint())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "images",
		"--format", "{{json .}}").Output()
	if err != nil {
		return nil, c.wrapDockerErr(err)
	}
	var list []ImageInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r struct {
			ID         string `json:"ID"`
			Repository string `json:"Repository"`
			Tag        string `json:"Tag"`
			Size       string `json:"Size"`
		}
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		list = append(list, ImageInfo{
			ID: r.ID, Repo: r.Repository, Tag: r.Tag, Size: r.Size,
			Managed: strings.HasPrefix(r.Repository, "umbraforge/"),
		})
	}
	return list, nil
}

// containerAction 是允许对容器执行的动作。
type containerAction string

const (
	ActionStart   containerAction = "start"
	ActionStop    containerAction = "stop"
	ActionRestart containerAction = "restart"
	ActionRemove  containerAction = "remove"
)

// ContainerAction 对单个容器执行动作。
//
// 安全约束：删除只允许作用于 UmbraForge 自己创建的容器（名字前缀白名单）。
// 用户机器上往往还跑着 postgres/redis 等生产数据容器，一个手滑的删除不可逆，
// 所以非托管容器只允许启停/重启，不允许删。
func (c *Center) ContainerAction(name string, action containerAction) error {
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	if name == "" {
		return fmt.Errorf("容器名为空")
	}
	// 顺序很重要：先做「动作合法 + 是否允许删」的校验，再探守护进程。
	// 反过来的话，守护进程掉线时对 red-postgres 发删除请求会回「Docker 掉线」，
	// 而不是「拒绝删除非托管容器」—— 用户会以为修好 Docker 就能删掉它。
	// 安全边界不该依赖守护进程的存活状态。
	var args []string
	switch action {
	case ActionStart:
		args = []string{"start", name}
	case ActionStop:
		args = []string{"stop", name}
	case ActionRestart:
		args = []string{"restart", name}
	case ActionRemove:
		if !isManagedContainer(name) {
			return fmt.Errorf("拒绝删除非 UmbraForge 创建的容器 %q —— "+
				"它可能是你其它项目的数据容器。如确需删除请用 docker rm 手动操作", name)
		}
		args = []string{"rm", "-f", name}
	default:
		return fmt.Errorf("不支持的动作 %q", action)
	}
	if !c.daemonAlive() {
		return fmt.Errorf("%s", daemonDownHint())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), args...).CombinedOutput()
	if err != nil {
		return c.wrapDockerErr(fmt.Errorf("%v / %s", err, trunc(string(out), 200)))
	}
	// 容器被停/删后，插件状态要跟着回落，否则前端仍显示 docker 模式健康
	if pid := pluginOfContainer(name); pid != "" && (action == ActionStop || action == ActionRemove) {
		c.setMode(PluginID(pid), ModeLocal)
	}
	return nil
}

// EnsureCaptchaReady 开跑前自愈：确保该打码器可用。
//
// 触发场景：Docker 引擎重启（改 VM 规格 / colima restart / 开机）会把打码容器
// 停在 Exited(137)，而应用仍记着 docker 模式。此时每次解题都连不上端口，
// 整批注册全部失败，日志里只有解题超时，根因很难看出来。
//
// 处理顺序：已健康 → 直接返回；docker 模式容器停了 → 拉起来；
// 拉不起来 → 回落本机模式，至少能跑（打码器本身是内置的）。
func (c *Center) EnsureCaptchaReady(id string) (string, error) {
	pid := PluginID(id)
	switch pid {
	case PluginEzSolver, PluginVeloraTurn, PluginAuralith:
	default:
		return "", fmt.Errorf("未知打码插件 %q", id)
	}

	if c.Status(pid).Healthy {
		return "", nil
	}

	if c.mode(pid) == ModeDocker {
		name := c.dockerCaptchaName(pid)
		if c.daemonAlive() {
			if err := c.ContainerAction(name, ActionStart); err == nil {
				// 容器内 Chromium 已装好时几秒即可就绪
				for i := 0; i < 12; i++ {
					time.Sleep(2 * time.Second)
					if c.Status(pid).Healthy {
						return fmt.Sprintf("%s 容器此前已停止，已自动拉起并就绪", id), nil
					}
				}
			}
			// setMode 在 ContainerAction 里可能已被改动，这里明确回落
			c.setMode(pid, ModeDocker)
		}
		// Docker 起不来 → 回落本机
		if _, err := c.Install(InstallRequest{ID: pid, Mode: ModeLocal}); err != nil {
			return fmt.Sprintf("%s 的 Docker 容器无法启动，回落本机模式也失败", id), err
		}
		return fmt.Sprintf("%s 的 Docker 容器无法启动，已回落到本机模式继续注册", id), nil
	}

	// 本机模式没起来：直接拉起
	if _, err := c.Install(InstallRequest{ID: pid, Mode: ModeLocal}); err != nil {
		return fmt.Sprintf("%s 未运行，自动启动失败", id), err
	}
	return fmt.Sprintf("%s 此前未运行，已自动启动", id), nil
}

// ContainerActionByName 是给 HTTP 层用的字符串入口（校验动作合法性）。
func (c *Center) ContainerActionByName(name, action string) error {
	switch containerAction(strings.ToLower(strings.TrimSpace(action))) {
	case ActionStart:
		return c.ContainerAction(name, ActionStart)
	case ActionStop:
		return c.ContainerAction(name, ActionStop)
	case ActionRestart:
		return c.ContainerAction(name, ActionRestart)
	case ActionRemove:
		return c.ContainerAction(name, ActionRemove)
	default:
		return fmt.Errorf("不支持的动作 %q（只允许 start/stop/restart/remove）", action)
	}
}

// RemoveImage 删除镜像。只允许删 UmbraForge 自己 commit 的（umbraforge/*），
// 以及明确传入的公共基础镜像（用户主动点删除时）。
func (c *Center) RemoveImage(ref string, allowPublic bool) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("镜像名为空")
	}
	// 同上：安全校验先于守护进程探测
	if !strings.HasPrefix(ref, "umbraforge/") && !allowPublic {
		return fmt.Errorf("拒绝删除非 UmbraForge 镜像 %q（如确需删除请勾选允许删除公共镜像）", ref)
	}
	if !c.daemonAlive() {
		return fmt.Errorf("%s", daemonDownHint())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "rmi", "-f", ref).CombinedOutput()
	if err != nil {
		return c.wrapDockerErr(fmt.Errorf("%v / %s", err, trunc(string(out), 200)))
	}
	return nil
}

// DockerDiskUsage 返回 docker system df 的概要，供前端展示占用。
func (c *Center) DockerDiskUsage() (map[string]any, error) {
	if !c.daemonAlive() {
		return nil, fmt.Errorf("%s", daemonDownHint())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "system", "df",
		"--format", "{{json .}}").Output()
	if err != nil {
		return nil, c.wrapDockerErr(err)
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			rows = append(rows, m)
		}
	}
	return map[string]any{"rows": rows}, nil
}

// PruneDocker 清理未使用的镜像/容器/网络（不带 -a，不动正在用的）。
// 明确不加 --volumes：卷里可能是用户的数据库数据。
func (c *Center) PruneDocker() (string, error) {
	if !c.daemonAlive() {
		return "", fmt.Errorf("%s", daemonDownHint())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "system", "prune", "-f").CombinedOutput()
	if err != nil {
		return string(out), c.wrapDockerErr(err)
	}
	return strings.TrimSpace(string(out)), nil
}
