package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/umbraforge/desktop/internal/procutil"
	"golang.org/x/net/proxy"
)

const KeyWarpInstances = "warp_instances"
const KeyWarpLicense = "warp_license_key"
const KeyGlobalProxy = "global_proxy"
const warpImagePrimary = "monius/docker-warp-socks:latest"
const warpImageFallback = "dublok/cloudflare-warp:latest"
const warpContainerPort = 9091
const warpBasePort = 41000

type WarpInstance struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Port        int    `json:"port"`
	Container   string `json:"container"`
	ProxyURL    string `json:"proxy_url"` // local socks for registration: socks5://127.0.0.1:port
	Status      string `json:"status"`
	StatusLabel string `json:"status_label,omitempty"`
	PublicIP    string `json:"public_ip,omitempty"`
	Arch        string `json:"arch"`
	DataDir     string `json:"data_dir,omitempty"`
	HasPlus     bool   `json:"has_plus"`
	CreatedAt   string `json:"created_at"`
	Message     string `json:"message,omitempty"`
	// UpstreamProxyMode: inherit (default 总代理) | custom (本实例独立) | none (容器直连CF，无上游)
	UpstreamProxyMode string `json:"upstream_proxy_mode,omitempty"`
	// UpstreamProxy: only when mode=custom, e.g. socks5://user:pass@host:port
	UpstreamProxy string `json:"upstream_proxy,omitempty"`
	// EffectiveUpstream is computed for UI (resolved URL or empty)
	EffectiveUpstream string `json:"effective_upstream,omitempty"`
}

type GlobalProxyConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"` // socks5:// or http://
	// ApplyToWarp: inject into WARP containers so tunnel can establish via 总代理
	ApplyToWarp bool `json:"apply_to_warp"`
	// ApplyToApp: host-side CF/API traffic uses 总代理
	ApplyToApp bool `json:"apply_to_app"`
	// ChainRegister: registration dials 总代理 first then WARP socks (only useful for non-loopback WARP)
	// For local WARP, registration still uses WARP directly so exit IP is WARP; container uses 总代理出网.
	ChainRegister bool `json:"chain_register"`
}

type WarpService struct {
	store    JSONStoreLike
	accounts *AccountService
	mu       sync.Mutex
}

type JSONStoreLike interface {
	GetValue(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

func NewWarpService(st JSONStoreLike, accounts *AccountService) *WarpService {
	return &WarpService{store: st, accounts: accounts}
}

func (w *WarpService) GetLicense(ctx context.Context) (string, error) {
	return w.store.GetValue(ctx, KeyWarpLicense)
}

func (w *WarpService) SaveLicense(ctx context.Context, key string) error {
	return w.store.Set(ctx, KeyWarpLicense, strings.TrimSpace(key))
}

func (w *WarpService) GetGlobalProxy(ctx context.Context) (GlobalProxyConfig, error) {
	raw, err := w.store.GetValue(ctx, KeyGlobalProxy)
	if err != nil {
		return GlobalProxyConfig{}, err
	}
	cfg := GlobalProxyConfig{ApplyToWarp: true, ApplyToApp: true}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg, nil
}

func (w *WarpService) SaveGlobalProxy(ctx context.Context, cfg GlobalProxyConfig) (GlobalProxyConfig, error) {
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.Enabled && cfg.URL == "" {
		return cfg, fmt.Errorf("启用总代理时必须填写代理 URL")
	}
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return cfg, fmt.Errorf("总代理 URL 无效，示例 socks5://127.0.0.1:7890 或 http://user:pass@host:8080")
		}
		sch := strings.ToLower(u.Scheme)
		if sch != "socks5" && sch != "socks5h" && sch != "http" && sch != "https" {
			return cfg, fmt.Errorf("总代理仅支持 socks5 / socks5h / http / https")
		}
	}
	b, _ := json.Marshal(cfg)
	if err := w.store.Set(ctx, KeyGlobalProxy, string(b)); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// StatusLabel maps docker state to Chinese for UI.
func StatusLabel(st string) string {
	switch strings.ToLower(strings.TrimSpace(st)) {
	case "running":
		return "运行中"
	case "exited", "dead":
		return "已停止"
	case "created", "restarting", "starting":
		return "启动中"
	case "paused":
		return "已暂停"
	case "missing", "":
		return "不存在"
	default:
		return st
	}
}

func (w *WarpService) List(ctx context.Context) ([]WarpInstance, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	list, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	needProbe := make([]WarpInstance, 0)
	for i := range list {
		list[i].Status = w.containerStatus(list[i].Container)
		list[i].StatusLabel = StatusLabel(list[i].Status)
		if list[i].UpstreamProxyMode == "" {
			list[i].UpstreamProxyMode = "inherit"
		}
		eu, _ := w.resolveUpstreamURL(ctx, &list[i])
		list[i].EffectiveUpstream = eu
		// quick non-blocking: only mark status; probe missing IPs async
		if list[i].Status == "running" && strings.TrimSpace(list[i].PublicIP) == "" {
			needProbe = append(needProbe, list[i])
			if list[i].Message == "" {
				list[i].Message = "waiting_ip"
			}
		}
		if list[i].Status != "running" && list[i].Status != "starting" {
			// keep last known IP but note status
			if list[i].Message == "" {
				list[i].Message = list[i].Status
			}
		}
	}
	_ = w.save(ctx, list)
	if len(needProbe) > 0 {
		go w.probeCreated(append([]WarpInstance{}, needProbe...))
	}
	return list, nil
}

func (w *WarpService) load(ctx context.Context) ([]WarpInstance, error) {
	raw, err := w.store.GetValue(ctx, KeyWarpInstances)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return []WarpInstance{}, nil
	}
	var list []WarpInstance
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (w *WarpService) save(ctx context.Context, list []WarpInstance) error {
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return w.store.Set(ctx, KeyWarpInstances, string(b))
}

func (w *WarpService) DockerOK() bool {
	if _, err := exec.LookPath(dockerBin()); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return procutil.CommandContext(ctx, dockerBin(), "info").Run() == nil
}

func (w *WarpService) nextPort(list []WarpInstance) int {
	used := map[int]bool{}
	for _, it := range list {
		used[it.Port] = true
	}
	for p := warpBasePort; p < warpBasePort+300; p++ {
		if used[p] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p
	}
	return warpBasePort + len(list)
}

func warpDataRoot() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "UmbraForge", "warp-data")
	case "windows":
		if app := os.Getenv("APPDATA"); app != "" {
			return filepath.Join(app, "UmbraForge", "warp-data")
		}
		return filepath.Join(home, "UmbraForge", "warp-data")
	default:
		return filepath.Join(home, ".config", "umbraforge", "warp-data")
	}
}

// DeployRequest allows count + optional license override.
type DeployRequest struct {
	Count   int    `json:"count"`
	License string `json:"license_key"`
}

// Deploy creates n independent WARP containers (each own data dir => own identity/IP).
func (w *WarpService) Deploy(ctx context.Context, n int, license string) ([]WarpInstance, error) {
	if n <= 0 {
		n = 1
	}
	if n > 10 {
		n = 10
	}
	if !w.DockerOK() {
		return nil, fmt.Errorf("未检测到 Docker。macOS/Windows 请先安装 Docker Desktop；Linux 请安装 docker 引擎")
	}
	// persist license if provided
	license = strings.TrimSpace(license)
	if license != "" {
		_ = w.SaveLicense(ctx, license)
	} else {
		saved, _ := w.GetLicense(ctx)
		license = strings.TrimSpace(saved)
	}

	// pull image before lock (can be slow)
	img := ensureWarpImage()

	w.mu.Lock()
	defer w.mu.Unlock()
	list, err := w.load(ctx)
	if err != nil {
		return nil, err
	}

	created := make([]WarpInstance, 0, n)
	arch := runtime.GOARCH
	root := warpDataRoot()
	_ = os.MkdirAll(root, 0o755)

	for i := 0; i < n; i++ {
		port := w.nextPort(append(list, created...))
		name := fmt.Sprintf("umbra-warp-%d", port)
		dataDir := filepath.Join(root, name)
		_ = os.MkdirAll(dataDir, 0o755)
		_ = dockerCmd("rm", "-f", name).Run()

		args := []string{
			"run", "-d",
			"--name", name,
			"--restart", "unless-stopped",
			"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, warpContainerPort),
			"-v", dataDir + ":/data",
			"-e", "NET_PORT=" + strconv.Itoa(warpContainerPort),
		}
		args = append(args, w.warpContainerProxyArgsDefault(ctx)...)
		if license != "" {
			args = append(args,
				"-e", "WARP_LICENSE="+license,
				"-e", "WGCF_LICENSE_KEY="+license,
				"-e", "WARP_LICENSE_KEY="+license,
			)
		}
		args = append(args, img)

		out, err := dockerCmd(args...).CombinedOutput()
		msg := strings.TrimSpace(string(out))
		if err != nil {
			// retry with NET_ADMIN for images that need it
			args2 := []string{
				"run", "-d",
				"--name", name,
				"--restart", "unless-stopped",
				"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, warpContainerPort),
				"-v", dataDir + ":/data",
				"--cap-add", "NET_ADMIN",
				"--sysctl", "net.ipv6.conf.all.disable_ipv6=0",
				"-e", "NET_PORT=" + strconv.Itoa(warpContainerPort),
			}
			args2 = append(args2, w.warpContainerProxyArgsDefault(ctx)...)
			if license != "" {
				args2 = append(args2,
					"-e", "WARP_LICENSE="+license,
					"-e", "WGCF_LICENSE_KEY="+license,
					"-e", "WARP_LICENSE_KEY="+license,
				)
			}
			args2 = append(args2, img)
			_ = dockerCmd("rm", "-f", name).Run()
			out2, err2 := dockerCmd(args2...).CombinedOutput()
			msg = strings.TrimSpace(string(out2))
			err = err2
		}

		inst := WarpInstance{
			ID:                fmt.Sprintf("%d-%d", time.Now().UnixNano(), port),
			Name:              name,
			Port:              port,
			Container:         name,
			ProxyURL:          fmt.Sprintf("socks5://127.0.0.1:%d", port),
			Status:            "starting",
			Arch:              arch,
			DataDir:           dataDir,
			HasPlus:           license != "",
			CreatedAt:         time.Now().UTC().Format(time.RFC3339),
			UpstreamProxyMode: "inherit",
			Message:           msg,
		}
		if err != nil {
			inst.Status = "error"
			inst.Message = err.Error() + " | " + msg
		} else {
			inst.Status = "running"
		}
		created = append(created, inst)
		list = append(list, inst)
	}

	if err := w.save(ctx, list); err != nil {
		return created, err
	}

	// 并入代理池（作为 Source=warp 的一等条目，代理池页可见可启停）。
	//
	// 旧实现是往 ProxySettings.RegisterURLs 追加 URL。那条路是死的：代理池页
	// 渲染 Entries 而不是 register_urls，所以 WARP 从来不显示；且代理池页的
	// 出口设置自动保存不发 register_urls → Go 侧 nil → cleanURLs(nil) 返回空
	// → 每次自动保存都把刚加的 WARP URL 抹掉。详见 proxy_pool_warp.go。
	if w.accounts != nil {
		if _, err := w.accounts.SyncWarpProxyEntries(ctx, list); err != nil {
			log.Printf("[warp] 并入代理池失败（实例已创建，可在代理池页手动添加 %d 个出口）: %v",
				len(created), err)
		}
	}

	// async multi-stage IP probe per instance (each uses its own socks port)
	go w.probeCreated(append([]WarpInstance{}, created...))
	return created, nil
}

// RestartAll recreates all known WARP containers (same ports) so env like 总代理 is applied,
// then re-syncs the proxy pool. Prefer this after changing global proxy.
func (w *WarpService) RestartAll(ctx context.Context) ([]WarpInstance, error) {
	if !w.DockerOK() {
		return nil, fmt.Errorf("Docker 不可用（请确认 Docker Desktop 已启动）")
	}
	w.mu.Lock()
	list, err := w.load(ctx)
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}
	out, err := w.recreateInstances(ctx, list)
	if err != nil {
		return out, err
	}
	w.mu.Lock()
	_ = w.save(ctx, out)
	// sync pool
	if w.accounts != nil {
		ps, _ := w.accounts.GetProxySettings(ctx)
		seen := map[string]struct{}{}
		for _, u := range ps.RegisterURLs {
			seen[u] = struct{}{}
		}
		for _, c := range out {
			if c.ProxyURL == "" || c.Status == "error" {
				continue
			}
			if _, ok := seen[c.ProxyURL]; !ok {
				ps.RegisterURLs = append(ps.RegisterURLs, c.ProxyURL)
				seen[c.ProxyURL] = struct{}{}
			}
		}
		_, _ = w.accounts.SaveProxySettings(ctx, ps)
	}
	w.mu.Unlock()
	return out, nil
}

// SyncProxyPool pushes all running WARP proxy URLs into the register proxy pool.
func (w *WarpService) SyncProxyPool(ctx context.Context) (map[string]any, error) {
	list, err := w.List(ctx)
	if err != nil {
		return nil, err
	}
	if w.accounts == nil {
		return nil, fmt.Errorf("account service unavailable")
	}
	ps, _ := w.accounts.GetProxySettings(ctx)
	seen := map[string]struct{}{}
	for _, u := range ps.RegisterURLs {
		seen[u] = struct{}{}
	}
	added := 0
	urls := []string{}
	for _, c := range list {
		if c.ProxyURL == "" {
			continue
		}
		urls = append(urls, c.ProxyURL)
		if _, ok := seen[c.ProxyURL]; !ok {
			ps.RegisterURLs = append(ps.RegisterURLs, c.ProxyURL)
			seen[c.ProxyURL] = struct{}{}
			added++
		}
	}
	_, _ = w.accounts.SaveProxySettings(ctx, ps)
	return map[string]any{"added": added, "total_pool": len(ps.RegisterURLs), "warp_urls": urls}, nil
}

func ensureWarpImage() string {
	for _, img := range []string{warpImagePrimary, warpImageFallback} {
		if dockerCmd("image", "inspect", img).Run() == nil {
			return img
		}
		if dockerCmd("pull", img).Run() == nil {
			return img
		}
	}
	return warpImagePrimary
}

func (w *WarpService) probeCreated(items []WarpInstance) {
	// wait for warp inside container to register (WG handshake may need longer on restricted networks)
	delays := []time.Duration{3 * time.Second, 6 * time.Second, 10 * time.Second, 15 * time.Second}
	for _, wait := range delays {
		time.Sleep(wait)
		w.mu.Lock()
		cur, _ := w.load(context.Background())
		byName := map[string]int{}
		for i, it := range cur {
			byName[it.Name] = i
		}
		allHaveIP := true
		for _, it := range items {
			idx, ok := byName[it.Name]
			if !ok {
				continue
			}
			cur[idx].Status = w.containerStatus(it.Container)
			ip, note := w.probeIPWithContainer(it.ProxyURL, it.Container)
			if ip != "" {
				cur[idx].PublicIP = ip
				cur[idx].Message = note
				if note == "" {
					cur[idx].Message = "ip_ok"
				}
			} else {
				allHaveIP = false
				if note == "" {
					note = "waiting_ip"
				}
				cur[idx].Message = note
			}
		}
		_ = w.save(context.Background(), cur)
		w.mu.Unlock()
		if allHaveIP {
			return
		}
	}
}

func (w *WarpService) Remove(ctx context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	list, err := w.load(ctx)
	if err != nil {
		return err
	}
	out := list[:0]
	var removedURL, dataDir string
	for _, it := range list {
		if it.Name == name || it.ID == name {
			_ = dockerCmd("rm", "-f", it.Container).Run()
			removedURL = it.ProxyURL
			dataDir = it.DataDir
			continue
		}
		out = append(out, it)
	}
	if err := w.save(ctx, out); err != nil {
		return err
	}
	if dataDir != "" {
		_ = os.RemoveAll(dataDir)
	}
	if removedURL != "" && w.accounts != nil {
		// 容器已删，同步移除代理池里对应的 WARP 条目 —— 否则它会作为
		// 「僵尸出口」继续参与注册，每次取到都连不上。
		// SyncWarpProxyEntries 以 out（删除后的实例集合）为准，自动淘汰。
		if _, err := w.accounts.SyncWarpProxyEntries(ctx, out); err != nil {
			log.Printf("[warp] 移除 %s 后同步代理池失败: %v", name, err)
		}
		// 顺带清掉 register_urls 里的历史残留（旧版本往那里写过）
		ps, _ := w.accounts.GetProxySettings(ctx)
		filtered := make([]string, 0, len(ps.RegisterURLs))
		changed := false
		for _, u := range ps.RegisterURLs {
			if u == removedURL {
				changed = true
				continue
			}
			filtered = append(filtered, u)
		}
		if changed {
			ps.RegisterURLs = filtered
			_, _ = w.accounts.SaveProxySettings(ctx, ps)
		}
	}
	return nil
}

func (w *WarpService) RefreshIP(ctx context.Context, name string) (WarpInstance, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	list, err := w.load(ctx)
	if err != nil {
		return WarpInstance{}, err
	}
	for i, it := range list {
		if it.Name != name && it.ID != name {
			continue
		}
		// restart re-registers identity for monius image; also try warp-cli if present
		_ = dockerCmd("exec", it.Container, "sh", "-lc",
			"warp-cli --accept-tos disconnect 2>/dev/null || true; warp-cli --accept-tos connect 2>/dev/null || true; true").Run()
		_ = dockerCmd("restart", it.Container).Run()
		time.Sleep(4 * time.Second)
		var ip, note string
		for attempt := 0; attempt < 4; attempt++ {
			ip, note = w.probeIPWithContainer(it.ProxyURL, it.Container)
			if ip != "" {
				break
			}
			time.Sleep(2 * time.Second)
		}
		it.PublicIP = ip
		it.Status = w.containerStatus(it.Container)
		if ip == "" {
			if note == "" {
				note = "ip_probe_failed"
			}
			it.Message = note
		} else {
			if note == "" {
				note = "ip_refreshed"
			}
			it.Message = note
		}
		list[i] = it
		_ = w.save(ctx, list)
		return it, nil
	}
	return WarpInstance{}, fmt.Errorf("instance not found: %s", name)
}

func (w *WarpService) RefreshAll(ctx context.Context) ([]WarpInstance, error) {
	list, err := w.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WarpInstance, 0, len(list))
	for _, it := range list {
		got, err := w.RefreshIP(ctx, it.Name)
		if err != nil {
			it.Message = err.Error()
			out = append(out, it)
			continue
		}
		out = append(out, got)
	}
	return out, nil
}

func (w *WarpService) containerStatus(name string) string {
	out, err := dockerCmd("inspect", "-f", "{{.State.Status}}", name).CombinedOutput()
	if err != nil {
		// distinguish docker-not-found vs container missing
		msg := strings.ToLower(string(out) + err.Error())
		if strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file") {
			return "docker_unavailable"
		}
		if strings.Contains(msg, "cannot connect") || strings.Contains(msg, "is the docker daemon") {
			return "docker_down"
		}
		return "missing"
	}
	return strings.TrimSpace(string(out))
}

// probeIPWithContainer measures the *WARP exit IP* only.
// Correct path when 总代理 is enabled:
//
//	WARP container uses 总代理 to reach Cloudflare (tunnel / "allocate IP"),
//	then we query public IP *through the WARP SOCKS* so result is WARP IP, not 总代理 IP.
//	Host: app → socks5://127.0.0.1:<mapped> → WARP tunnel → ipify
//	Never use raw container egress as WARP IP (that bypasses WARP).
func (w *WarpService) probeIPWithContainer(proxyURL, container string) (string, string) {
	// 1) Host dial via WARP local SOCKS (preferred)
	if ip := probeIPViaSOCKS(proxyURL, 8*time.Second); ip != "" {
		return ip, "via_warp_socks"
	}

	// 2) Inside container: curl via WARP inbound 9091 (same exit as host mapping)
	if container != "" && containerStatusQuick(container) == "running" {
		// nudge warp-cli connect (tunnel may need 总代理 env already in container)
		(func() {
			up := ""
			if list, err := w.load(context.Background()); err == nil {
				for i := range list {
					if list[i].Container == container || list[i].Name == container {
						up, _ = w.resolveUpstreamURL(context.Background(), &list[i])
						break
					}
				}
			}
			w.nudgeWarpConnect(container, up)
		})()
		if ip := probeViaDockerExecWarpSocks(container); ip != "" {
			return ip, "via_warp_container_socks"
		}
		// Do NOT return direct container egress as assigned WARP IP.
		return "", "warp_tunnel_pending"
	}
	return "", "ip_probe_failed"
}

// nudgeWarpConnect best-effort re-connect inside container (uses container env ALL_PROXY/HTTP_PROXY if set).
func (w *WarpService) nudgeWarpConnect(container string, upstream string) {
	extra := ""
	u := strings.TrimSpace(upstream)
	if u == "" {
		cfg, _ := w.GetGlobalProxy(context.Background())
		if cfg.Enabled && cfg.ApplyToWarp {
			u = strings.TrimSpace(cfg.URL)
		}
	}
	if u != "" {
		extra = fmt.Sprintf(
			`export HTTP_PROXY=%q HTTPS_PROXY=%q ALL_PROXY=%q http_proxy=%q https_proxy=%q all_proxy=%q; `,
			u, u, u, u, u, u,
		)
	}
	script := extra + `warp-cli --accept-tos connect 2>/dev/null || true; sleep 1; true`
	_ = dockerCmd("exec", container, "sh", "-lc", script).Run()
}

// probeViaDockerExecWarpSocks forces traffic through WARP's local socks inside the container.
func probeViaDockerExecWarpSocks(container string) string {
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}
	for _, ep := range endpoints {
		// only via WARP socks/http on container port — not direct egress
		script := fmt.Sprintf(
			`curl -sS --max-time 12 -x socks5h://127.0.0.1:%d %q 2>/dev/null || curl -sS --max-time 12 -x http://127.0.0.1:%d %q 2>/dev/null || true`,
			warpContainerPort, ep, warpContainerPort, ep,
		)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := procutil.CommandContext(ctx, dockerBin(), "exec", container, "sh", "-lc", script).CombinedOutput()
		cancel()
		if err != nil {
			continue
		}
		if ip := extractIP(string(out)); ip != "" {
			return ip
		}
	}
	// cloudflare trace via warp socks
	script := fmt.Sprintf(
		`curl -sS --max-time 12 -x socks5h://127.0.0.1:%d https://www.cloudflare.com/cdn-cgi/trace 2>/dev/null || true`,
		warpContainerPort,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	out, _ := procutil.CommandContext(ctx, dockerBin(), "exec", container, "sh", "-lc", script).CombinedOutput()
	cancel()
	return extractIPFromCFTrace(string(out))
}

// probeIPViaSOCKS queries public IP through a SOCKS/HTTP proxy URL (WARP host mapping).
func probeIPViaSOCKS(proxyURL string, timeout time.Duration) string {
	return probeIPGoTimeout(proxyURL, []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}, timeout)
}

func extractIP(s string) string {
	s = strings.TrimSpace(s)
	// plain IP response
	if ip := net.ParseIP(strings.TrimSpace(strings.Split(s, "\n")[0])); ip != nil {
		return ip.String()
	}
	return extractIPFromCFTrace(s)
}

func extractIPFromCFTrace(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			ip := strings.TrimPrefix(line, "ip=")
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	// fallback regex-like scan
	for _, f := range strings.Fields(s) {
		if net.ParseIP(f) != nil {
			return f
		}
	}
	return ""
}

func containerStatusQuick(name string) string {
	out, err := dockerCmd("inspect", "-f", "{{.State.Status}}", name).CombinedOutput()
	if err != nil {
		return "missing"
	}
	return strings.TrimSpace(string(out))
}

func probeViaDockerExec(container string, throughProxy bool) string {
	if _, err := exec.LookPath(dockerBin()); err != nil {
		return ""
	}
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://www.cloudflare.com/cdn-cgi/trace",
	}
	for _, ep := range endpoints {
		var script string
		if throughProxy {
			// monius mixed inbound on 9091
			script = fmt.Sprintf(
				`curl -sS --max-time 10 -x socks5h://127.0.0.1:%d %q 2>/dev/null || curl -sS --max-time 10 -x http://127.0.0.1:%d %q 2>/dev/null || true`,
				warpContainerPort, ep, warpContainerPort, ep,
			)
		} else {
			script = fmt.Sprintf(`curl -sS --max-time 8 %q 2>/dev/null || true`, ep)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		out, err := procutil.CommandContext(ctx, dockerBin(), "exec", container, "sh", "-lc", script).CombinedOutput()
		cancel()
		if err != nil && len(out) == 0 {
			continue
		}
		if ip := extractIP(string(out)); ip != "" {
			return ip
		}
	}
	return ""
}

// probeIP queries public IP through the instance's own proxy endpoint from host.
func probeIPFast(proxyURL string, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://www.cloudflare.com/cdn-cgi/trace",
	}
	// curl only, short timeout
	if _, err := exec.LookPath("curl"); err != nil {
		return probeIPGoTimeout(proxyURL, endpoints, timeout)
	}
	hostPort := strings.TrimPrefix(proxyURL, "socks5://")
	hostPort = strings.TrimPrefix(hostPort, "socks5h://")
	hostPort = strings.TrimPrefix(hostPort, "http://")
	sec := int(timeout.Seconds())
	if sec < 2 {
		sec = 2
	}
	modes := [][]string{
		{"--socks5-hostname", hostPort},
		{"-x", "http://" + hostPort},
	}
	for _, mode := range modes {
		for _, ep := range endpoints {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			args := append([]string{"-sS", "--max-time", strconv.Itoa(sec)}, mode...)
			args = append(args, ep)
			out, err := procutil.CommandContext(ctx, "curl", args...).CombinedOutput()
			cancel()
			if err != nil {
				continue
			}
			if ip := extractIP(string(out)); ip != "" {
				return ip
			}
		}
	}
	return probeIPGoTimeout(proxyURL, endpoints, timeout)
}

func probeIPGoTimeout(proxyURL string, endpoints []string, timeout time.Duration) string {
	if !strings.Contains(proxyURL, "://") {
		return ""
	}
	// support socks and http
	candidates := []string{proxyURL}
	if strings.HasPrefix(proxyURL, "socks5://") || strings.HasPrefix(proxyURL, "socks5h://") {
		hp := strings.TrimPrefix(proxyURL, "socks5://")
		hp = strings.TrimPrefix(hp, "socks5h://")
		candidates = append(candidates, "http://"+hp)
	}
	for _, cand := range candidates {
		u, err := url.Parse(cand)
		if err != nil {
			continue
		}
		var tr *http.Transport
		if u.Scheme == "http" || u.Scheme == "https" {
			tr = &http.Transport{Proxy: http.ProxyURL(u)}
		} else {
			dialer, err := proxy.FromURL(u, proxy.Direct)
			if err != nil {
				continue
			}
			tr = &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			}
		}
		client := &http.Client{Transport: tr, Timeout: timeout}
		for _, ep := range endpoints {
			resp, err := client.Get(ep)
			if err != nil {
				continue
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if ip := extractIP(string(raw)); ip != "" {
				return ip
			}
		}
	}
	return ""
}

func probeIP(proxyURL string) string {
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
		"https://www.cloudflare.com/cdn-cgi/trace",
	}
	if ip := probeIPGo(proxyURL, endpoints); ip != "" {
		return ip
	}
	// also try http proxy form (monius mixed)
	if strings.HasPrefix(proxyURL, "socks5://") || strings.HasPrefix(proxyURL, "socks5h://") {
		hp := strings.TrimPrefix(proxyURL, "socks5://")
		hp = strings.TrimPrefix(hp, "socks5h://")
		if ip := probeIPGo("http://"+hp, endpoints); ip != "" {
			return ip
		}
	}
	if _, err := exec.LookPath("curl"); err != nil {
		return ""
	}
	hostPort := strings.TrimPrefix(proxyURL, "socks5://")
	hostPort = strings.TrimPrefix(hostPort, "socks5h://")
	hostPort = strings.TrimPrefix(hostPort, "http://")
	modes := [][]string{
		{"--socks5-hostname", hostPort},
		{"-x", "http://" + hostPort},
	}
	for _, mode := range modes {
		for _, ep := range endpoints {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			args := append([]string{"-sS", "--max-time", "10"}, mode...)
			args = append(args, ep)
			out, err := procutil.CommandContext(ctx, "curl", args...).CombinedOutput()
			cancel()
			if err != nil {
				continue
			}
			if ip := extractIP(string(out)); ip != "" {
				return ip
			}
		}
	}
	return ""
}

func probeIPGo(proxyURL string, endpoints []string) string {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return ""
	}
	dialer, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return ""
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}
	for _, ep := range endpoints {
		resp, err := client.Get(ep)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if ip := extractIP(string(raw)); ip != "" {
			return ip
		}
	}
	return ""
}

func HostCapabilities() map[string]any {
	docker := false
	if _, err := exec.LookPath(dockerBin()); err == nil {
		docker = dockerCmd("info").Run() == nil
	}
	warpCLI := false
	if _, err := exec.LookPath("warp-cli"); err == nil {
		warpCLI = true
	}
	return map[string]any{
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"docker":     docker,
		"warp_cli":   warpCLI,
		"warp_image": warpImagePrimary,
		"base_port":  warpBasePort,
		"multi_ip":   docker,
		"hint":       warpHint(),
	}
}

func warpHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "每个 WARP 容器独立注册身份+独立本地 SOCKS 端口；出口 IP 经该实例代理自动探测（通常互不相同）。可选 Warp+ Key。"
	case "linux":
		return "多实例各自独立注册；刷 IP 会对容器内 warp-cli reconnect。可选 Warp+ Key。"
	case "windows":
		return "需 Docker Desktop。多实例映射 127.0.0.1 不同端口，IP 按实例代理探测。"
	default:
		return "请安装 Docker 后使用。"
	}
}

func ParseCount(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// resolveUpstreamURL returns upstream proxy for a WARP instance.
// mode: inherit|"" → default 总代理 (if enabled+apply_to_warp); custom → instance URL; none → no upstream.
func (w *WarpService) resolveUpstreamURL(ctx context.Context, it *WarpInstance) (url string, mode string) {
	mode = strings.ToLower(strings.TrimSpace(it.UpstreamProxyMode))
	if mode == "" {
		mode = "inherit"
	}
	switch mode {
	case "none", "off", "direct":
		return "", "none"
	case "custom":
		u := strings.TrimSpace(it.UpstreamProxy)
		return u, "custom"
	default: // inherit
		cfg, err := w.GetGlobalProxy(ctx)
		if err != nil || !cfg.Enabled || !cfg.ApplyToWarp {
			return "", "inherit"
		}
		return strings.TrimSpace(cfg.URL), "inherit"
	}
}

// warpContainerProxyArgs builds docker -e pairs for a specific instance's upstream.
func (w *WarpService) warpContainerProxyArgs(ctx context.Context, it *WarpInstance) []string {
	u, _ := w.resolveUpstreamURL(ctx, it)
	if u == "" {
		return nil
	}
	return []string{
		"-e", "HTTP_PROXY=" + u,
		"-e", "HTTPS_PROXY=" + u,
		"-e", "ALL_PROXY=" + u,
		"-e", "http_proxy=" + u,
		"-e", "https_proxy=" + u,
		"-e", "all_proxy=" + u,
		"-e", "NO_PROXY=localhost,127.0.0.1",
		"-e", "no_proxy=localhost,127.0.0.1",
	}
}

// warpContainerProxyArgsDefault uses global default only (for brand-new instances before saved).
func (w *WarpService) warpContainerProxyArgsDefault(ctx context.Context) []string {
	dummy := &WarpInstance{UpstreamProxyMode: "inherit"}
	return w.warpContainerProxyArgs(ctx, dummy)
}

// UpdateInstanceUpstream sets per-WARP upstream mode/url and optionally recreates container.
func (w *WarpService) UpdateInstanceUpstream(ctx context.Context, name, mode, upstream string, recreate bool) (WarpInstance, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "inherit"
	}
	if mode != "inherit" && mode != "custom" && mode != "none" {
		return WarpInstance{}, fmt.Errorf("upstream_proxy_mode 须为 inherit | custom | none")
	}
	upstream = strings.TrimSpace(upstream)
	if mode == "custom" {
		if upstream == "" {
			return WarpInstance{}, fmt.Errorf("独立总代理模式必须填写 URL")
		}
		u, err := url.Parse(upstream)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return WarpInstance{}, fmt.Errorf("独立总代理 URL 无效")
		}
	}

	w.mu.Lock()
	list, err := w.load(ctx)
	if err != nil {
		w.mu.Unlock()
		return WarpInstance{}, err
	}
	idx := -1
	for i, it := range list {
		if it.Name == name || it.ID == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		w.mu.Unlock()
		return WarpInstance{}, fmt.Errorf("instance not found: %s", name)
	}
	list[idx].UpstreamProxyMode = mode
	if mode == "custom" {
		list[idx].UpstreamProxy = upstream
	} else if mode != "custom" {
		// keep stored custom url for convenience when switching back, or clear:
		// list[idx].UpstreamProxy = list[idx].UpstreamProxy
	}
	if mode == "none" {
		// leave UpstreamProxy as-is for later reuse
	}
	_ = w.save(ctx, list)
	it := list[idx]
	w.mu.Unlock()

	if recreate {
		return w.RecreateOne(ctx, it.Name)
	}
	u, _ := w.resolveUpstreamURL(ctx, &it)
	it.EffectiveUpstream = u
	it.Status = w.containerStatus(it.Container)
	it.StatusLabel = StatusLabel(it.Status)
	return it, nil
}

// RecreateOne rebuilds one container applying its resolved upstream proxy env.
func (w *WarpService) RecreateOne(ctx context.Context, name string) (WarpInstance, error) {
	if !w.DockerOK() {
		return WarpInstance{}, fmt.Errorf("Docker 不可用")
	}
	w.mu.Lock()
	list, err := w.load(ctx)
	if err != nil {
		w.mu.Unlock()
		return WarpInstance{}, err
	}
	var it WarpInstance
	idx := -1
	for i := range list {
		if list[i].Name == name || list[i].ID == name {
			it = list[i]
			idx = i
			break
		}
	}
	if idx < 0 {
		w.mu.Unlock()
		return WarpInstance{}, fmt.Errorf("instance not found: %s", name)
	}
	w.mu.Unlock()

	// reuse RestartAll logic for single item by temporarily saving only... call internal
	all, err := w.recreateInstances(ctx, []WarpInstance{it})
	if err != nil {
		return WarpInstance{}, err
	}
	if len(all) == 0 {
		return WarpInstance{}, fmt.Errorf("recreate failed")
	}
	// merge back into full list
	w.mu.Lock()
	defer w.mu.Unlock()
	full, err := w.load(ctx)
	if err != nil {
		return all[0], err
	}
	for i := range full {
		if full[i].Name == all[0].Name {
			full[i] = all[0]
			break
		}
	}
	_ = w.save(ctx, full)
	return all[0], nil
}

func (w *WarpService) recreateInstances(ctx context.Context, items []WarpInstance) ([]WarpInstance, error) {
	license, _ := w.GetLicense(ctx)
	img := ensureWarpImage()
	out := make([]WarpInstance, 0, len(items))
	for _, it := range items {
		name := it.Name
		port := it.Port
		if name == "" || port <= 0 {
			out = append(out, it)
			continue
		}
		_ = dockerCmd("rm", "-f", name).Run()
		dataDir := it.DataDir
		if dataDir == "" {
			dataDir = filepath.Join(warpDataRoot(), name)
		}
		_ = os.MkdirAll(dataDir, 0o755)
		proxyArgs := w.warpContainerProxyArgs(ctx, &it)
		args := []string{
			"run", "-d",
			"--name", name,
			"--restart", "unless-stopped",
			"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, warpContainerPort),
			"-v", dataDir + ":/data",
			"-e", "NET_PORT=" + strconv.Itoa(warpContainerPort),
		}
		args = append(args, proxyArgs...)
		if strings.TrimSpace(license) != "" || it.HasPlus {
			lic := strings.TrimSpace(license)
			if lic != "" {
				args = append(args,
					"-e", "WARP_LICENSE="+lic,
					"-e", "WGCF_LICENSE_KEY="+lic,
					"-e", "WARP_LICENSE_KEY="+lic,
				)
			}
		}
		args = append(args, img)
		bout, berr := dockerCmd(args...).CombinedOutput()
		if berr != nil {
			_ = dockerCmd("rm", "-f", name).Run()
			args2 := []string{
				"run", "-d",
				"--name", name,
				"--restart", "unless-stopped",
				"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, warpContainerPort),
				"-v", dataDir + ":/data",
				"--cap-add", "NET_ADMIN",
				"-e", "NET_PORT=" + strconv.Itoa(warpContainerPort),
			}
			args2 = append(args2, proxyArgs...)
			if strings.TrimSpace(license) != "" {
				lic := strings.TrimSpace(license)
				args2 = append(args2,
					"-e", "WARP_LICENSE="+lic,
					"-e", "WGCF_LICENSE_KEY="+lic,
					"-e", "WARP_LICENSE_KEY="+lic,
				)
			}
			args2 = append(args2, img)
			bout, berr = dockerCmd(args2...).CombinedOutput()
		}
		it.ProxyURL = fmt.Sprintf("socks5://127.0.0.1:%d", port)
		it.Container = name
		if berr != nil {
			it.Status = "error"
			it.Message = berr.Error() + " | " + strings.TrimSpace(string(bout))
		} else {
			it.Status = "running"
			it.Message = "recreated"
			it.PublicIP = ""
		}
		it.StatusLabel = StatusLabel(it.Status)
		u, _ := w.resolveUpstreamURL(ctx, &it)
		it.EffectiveUpstream = u
		out = append(out, it)
	}
	go w.probeCreated(append([]WarpInstance{}, out...))
	return out, nil
}
