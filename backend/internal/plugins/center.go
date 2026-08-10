package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/umbraforge/desktop/internal/procutil"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	ModeLocal  Mode = "local"
	ModeDocker Mode = "docker"
)

type PluginID string

const (
	PluginEzSolver   PluginID = "ezsolver"
	PluginVeloraTurn PluginID = "veloraturn"
	PluginAuralith   PluginID = "auralith"
)

type PluginInfo struct {
	ID          PluginID `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	DefaultPort int      `json:"default_port"`
	HealthPath  string   `json:"health_path"`
	Modes       []Mode   `json:"modes"`
}

type PluginStatus struct {
	ID        PluginID `json:"id"`
	Mode      Mode     `json:"mode"`
	Installed bool     `json:"installed"`
	Running   bool     `json:"running"`
	Healthy   bool     `json:"healthy"`
	URL       string   `json:"url"`
	Message   string   `json:"message,omitempty"`
	DockerOK  bool     `json:"docker_ok"`
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
}

type InstallRequest struct {
	ID   PluginID `json:"id"`
	Mode Mode     `json:"mode"`
	Port int      `json:"port,omitempty"`
}

type Center struct {
	mu         sync.Mutex
	root       string // umbraforge root (plugins dir parent)
	stateRoot  string // mutable logs/venvs; outside a protected payload root
	states     map[PluginID]Mode
	procs      map[PluginID]*exec.Cmd
	dockerName map[PluginID]string
	// tasks 记录 Docker 安装 / 打码容器部署的异步进度（前端轮询展示）。
	// key: "docker-install"（装 Docker）与 "docker-deploy:<pluginID>"（部署打码容器）。
	tasks map[string]*PluginTask
	// maxWorkers 由外部注入（engine 配置）——打码并发数，避免 auto-tune 启动即拉满浏览器。
	// 用独立的 workersMu 而不是 mu：读取点同时存在于持 mu 的路径（Install →
	// installLocal → applyWorkerEnv）和不持 mu 的路径（runDockerDeploy 协程），
	// 复用 mu 会在前者上自锁（Go 的 sync.Mutex 不可重入）。
	workersMu  sync.Mutex
	maxWorkers map[PluginID]int

	// specMu / spec* 缓存 Docker 引擎规格（见 capacity.go）。
	specMu    sync.Mutex
	specCores int
	specMemMB int
	specAt    time.Time

	// availMu / avail* 缓存 DockerAvailable() 的结果（2 秒 TTL）。
	// Status() 每次要问三遍，前端每 4~5 秒又轮询 3 个引擎——不缓存就是
	// 每轮 9 次 `docker info`。
	availMu sync.Mutex
	availOK bool
	availAt time.Time

	// stateMu 保护 states / dockerName / procs。
	// 同样不能用 mu：这三张表既在持 mu 的路径里写（installLocal / installDocker /
	// Stop），也在不持 mu 的地方读写（runDockerDeploy 协程写 states/dockerName、
	// Status 被 HTTP 处理协程直接调用读 states）。此前两边都裸访问同一张 map，
	// 部署容器时前端每秒轮询 /plugins/status，Go 运行时会以
	// "concurrent map read and map write" 直接终止进程。
	stateMu sync.Mutex
	// nanny 把本机打码 Chrome 窗口缩小挪到角落并还原焦点（仅 macOS 生效）
	nanny *WindowNanny
}

// PluginTask 是异步安装/部署任务的状态快照，前端轮询 GET /plugins/docker-task 展示进度。
type PluginTask struct {
	Key     string    `json:"key"`
	Running bool      `json:"running"`
	Stage   string    `json:"stage"` // detect|install|wait-daemon|pull|deploy|health|ready|failed
	Message string    `json:"message"`
	Log     []string  `json:"log"`
	Done    bool      `json:"done"`
	OK      bool      `json:"ok"`
	OS      string    `json:"os"`
	Arch    string    `json:"arch"`
	Started time.Time `json:"started_at"`
	Updated time.Time `json:"updated_at"`
	// InstallerFiles 卸载成功后残留的安装器下载缓存（Windows），
	// 前端据此弹窗询问是否删除。
	InstallerFiles []map[string]any `json:"installer_files,omitempty"`
}

// dockerAvailTTL 是 DockerAvailable() 结果的缓存时长。取 2 秒：远短于前端
// 4~5 秒的轮询间隔（状态翻转下一拍就可见），又足以把单次轮询里的重复
// `docker info` 收敛成一次。
const dockerAvailTTL = 2 * time.Second

const taskKeyDockerInstall = "docker-install"

// taskKeyDeployAll 是一键部署全部打码引擎（3 个容器）的批量任务 key。
const taskKeyDeployAll = "docker-deploy:all"

// captchaDeployOrder 一键部署的执行顺序：先轻后重（Auralith 预编译二进制 →
// VeloraTurn Go → EzSolver Python 首次要建 venv），保证大部分引擎尽快就绪。
var captchaDeployOrder = []PluginID{PluginAuralith, PluginVeloraTurn, PluginEzSolver}

func deployTaskKey(id PluginID) string { return "docker-deploy:" + string(id) }

func NewCenter(umbraRoot string) *Center {
	return NewCenterWithState(umbraRoot, filepath.Join(umbraRoot, "plugins", ".state"))
}

// NewCenterWithState keeps mutable process state outside the signed payload
// tree. Protected builds pass a private application-data directory here so an
// added sitecustomize.py, .pth, virtualenv binary, or log is never accepted as
// part of an otherwise verified plugin cache.
func NewCenterWithState(umbraRoot, stateRoot string) *Center {
	return &Center{
		root:      umbraRoot,
		stateRoot: stateRoot,
		states: map[PluginID]Mode{
			PluginEzSolver:   ModeLocal,
			PluginVeloraTurn: ModeLocal,
			PluginAuralith:   ModeLocal,
		},
		procs:      map[PluginID]*exec.Cmd{},
		dockerName: map[PluginID]string{},
		tasks:      map[string]*PluginTask{},
		maxWorkers: map[PluginID]int{},
		nanny:      NewWindowNanny(),
	}
}

func (c *Center) mutableStateRoot() string {
	if strings.TrimSpace(c.stateRoot) != "" {
		return c.stateRoot
	}
	return filepath.Join(c.root, "plugins", ".state")
}

func (c *Center) runLogDir() string {
	return filepath.Join(c.mutableStateRoot(), "runlogs")
}

func (c *Center) ezSolverVenvDir() string {
	return filepath.Join(c.mutableStateRoot(), "ezsolver", ".venv")
}

// Task 返回单个异步任务快照（不存在则返回空任务）。
func (c *Center) Task(key string) PluginTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tasks[key]
	if !ok {
		return PluginTask{Key: key}
	}
	return *t
}

// Tasks 返回全部异步任务快照（Docker 安装 + 各插件容器部署）。
func (c *Center) Tasks() map[string]PluginTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]PluginTask, len(c.tasks))
	for k, t := range c.tasks {
		if t != nil {
			out[k] = *t
		}
	}
	return out
}

func (c *Center) taskStage(key, stage, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.tasks[key]; t != nil {
		t.Stage = stage
		t.Message = message
		t.Updated = time.Now()
	}
}

func (c *Center) taskLog(key, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.tasks[key]; t != nil {
		t.Log = append(t.Log, line)
		if len(t.Log) > 200 {
			t.Log = t.Log[len(t.Log)-200:]
		}
		t.Updated = time.Now()
	}
}

func (c *Center) taskFinish(key string, ok bool, stage, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.tasks[key]; t != nil {
		t.Running = false
		t.Done = true
		t.OK = ok
		t.Stage = stage
		t.Message = message
		t.Updated = time.Now()
	}
}

// runStreaming 执行外部命令并把 stdout/stderr 逐行写入任务日志。
func (c *Center) runStreaming(key string, name string, args []string, dir string, env []string) error {
	cmd := procutil.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			c.taskLog(key, sc.Text())
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	err = cmd.Wait()
	wg.Wait()
	return err
}

// SetMaxWorkers 注入打码并发上限（来自注册配置），启动打码进程时传给对应引擎。
// n<=0 表示「未设置 / auto」，会清掉旧值——旧实现用 if n>0 守卫，于是
// auralith_max_concurrency=0（默认值，含义是 auto）根本写不进来，
// MAX_WORKERS 永远缺失，auralithd 按 CPU auto-tune 直接开 18 个浏览器。
func (c *Center) SetMaxWorkers(id PluginID, n int) {
	c.workersMu.Lock()
	defer c.workersMu.Unlock()
	if c.maxWorkers == nil {
		c.maxWorkers = map[PluginID]int{}
	}
	if n > 0 {
		c.maxWorkers[id] = n
		return
	}
	delete(c.maxWorkers, id)
}

// MaxWorkers 返回已注入的打码并发上限（0 = 未设置，交给引擎自身 auto-tune）。
// 只锁 workersMu：本函数会在持有 c.mu 的安装路径里被调用。
func (c *Center) MaxWorkers(id PluginID) int {
	c.workersMu.Lock()
	defer c.workersMu.Unlock()
	return c.maxWorkers[id]
}

// ---- states / dockerName / procs 的受保护访问（见 stateMu 注释）----

func (c *Center) setMode(id PluginID, m Mode) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.states == nil {
		c.states = map[PluginID]Mode{}
	}
	c.states[id] = m
}

func (c *Center) mode(id PluginID) Mode {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.states[id]
}

func (c *Center) setDockerName(id PluginID, name string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.dockerName == nil {
		c.dockerName = map[PluginID]string{}
	}
	c.dockerName[id] = name
}

// takeProc 取出并移除已登记的本机进程（用于让出端口）。
func (c *Center) takeProc(id PluginID) *exec.Cmd {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	cmd := c.procs[id]
	delete(c.procs, id)
	return cmd
}

func (c *Center) setProc(id PluginID, cmd *exec.Cmd) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.procs == nil {
		c.procs = map[PluginID]*exec.Cmd{}
	}
	c.procs[id] = cmd
}

// applyWorkerEnv 把打码并发写进本机进程环境。三个引擎都读 MAX_WORKERS
// （EzSolver service.py、VeloraTurn main.go、auralithd 均已验证），
// 额外补一份引擎专属变量以防未来改名。
func (c *Center) applyWorkerEnv(id PluginID, env []string) []string {
	n := c.clampToCapacity(id, c.MaxWorkers(id))
	if n <= 0 {
		return env
	}
	env = upsertEnv(env, "MAX_WORKERS", fmt.Sprint(n))
	switch id {
	case PluginAuralith:
		env = upsertEnv(env, "AURALITH_MAX_WORKERS", fmt.Sprint(n))
	case PluginEzSolver:
		env = upsertEnv(env, "BROWSER_POOL", fmt.Sprint(n))
	}
	return env
}

// AutoStartLocalBundled 启动时自动以本地模式安装并启动内置免费打码插件
// （EzSolver / VeloraTurn / Auralith）。程序内置多架构二进制与 python 源码，
// 无需用户手动「安装」；个别失败仅记日志，不阻塞启动。
func (c *Center) AutoStartLocalBundled() {
	// 打码容器在跑时绝不能再起本机实例：8192/8193/8194 已被容器映射占着，
	// 本机进程只会抢端口失败或造成「端口有响应但容器状态是停」的假阳性。
	// 用户部署过 Docker 打码后重启 App 就会走到这里。
	// 用「容器存在（任意状态）」判断：容器存在（即使 stopped）就不启动本地，
	// 否则本地实例占住 8192/8193/8194，用户切回 Docker 时端口绑定失败，
	// 出现「设置 docker 模式、实际打码走本地进程」的假象。
	if c.captchaContainerExists() {
		log.Printf("[plugins] 检测到打码容器已存在（可能未运行），跳过本机模式自动启动；请到 Docker 管理启动或切换")
		return
	}
	for _, id := range []PluginID{PluginEzSolver, PluginVeloraTurn, PluginAuralith} {
		st := c.Status(id)
		if st.Installed && st.Running {
			continue
		}
		if _, err := c.Install(InstallRequest{ID: id, Mode: ModeLocal}); err != nil {
			log.Printf("[plugins] 自动启动内置打码 %s 失败: %v", id, err)
		}
	}
}

func (c *Center) Catalog() []PluginInfo {
	return []PluginInfo{
		{ID: PluginEzSolver, Name: "EzSolver Python", Description: "免费 Turnstile 打码（nodriver + Xvfb）", DefaultPort: 8192, HealthPath: "/health", Modes: []Mode{ModeLocal, ModeDocker}},
		{ID: PluginVeloraTurn, Name: "VeloraTurn Go", Description: "独立 Go 免费打码引擎", DefaultPort: 8193, HealthPath: "/health", Modes: []Mode{ModeLocal, ModeDocker}},
		{ID: PluginAuralith, Name: "Auralith", Description: "浏览器池免费打码", DefaultPort: 8194, HealthPath: "/health", Modes: []Mode{ModeLocal, ModeDocker}},
	}
}

// dockerBinaryPresent 只判断 docker 可执行文件在不在（不管守护进程）。
func (c *Center) dockerBinaryPresent() bool {
	bin := dockerExecutable()
	if bin == "docker" {
		_, err := exec.LookPath("docker")
		return err == nil
	}
	return fileExists(bin)
}

// DockerAvailable 判断 docker 可用（二进制存在 + 守护进程能连上）。
//
// 结果带 2 秒缓存：这个函数在 Status() 里被反复调用（DockerOK 字段、docker 模式
// 分支、effectiveMode 各一次），而前端每 4~5 秒轮询一次 /plugins → ListStatus()
// 又要跑 3 个引擎。不缓存的话单次轮询能打出 9 次 `docker info`，colima 上每次
// 100~300ms，整个接口要 600ms+。2 秒 TTL 远短于轮询间隔，用户点「启动虚拟机」
// 后下一拍就能看到状态翻转，不会出现「已就绪但界面还说未就绪」。
func (c *Center) DockerAvailable() bool {
	c.availMu.Lock()
	if time.Since(c.availAt) < dockerAvailTTL {
		v := c.availOK
		c.availMu.Unlock()
		return v
	}
	c.availMu.Unlock()

	ok := c.probeDockerAvailable()

	c.availMu.Lock()
	c.availOK, c.availAt = ok, time.Now()
	c.availMu.Unlock()
	return ok
}

// probeDockerAvailable 真正去探一次（不走缓存）。
func (c *Center) probeDockerAvailable() bool {
	if !c.dockerBinaryPresent() {
		return false
	}
	bin := dockerExecutable()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return procutil.CommandContext(ctx, bin, "info").Run() == nil
}

// invalidateDockerAvail 丢弃 DockerAvailable 缓存。装完 Docker / 起完虚拟机后
// 必须调用，否则最多 2 秒内仍会返回旧的「不可用」。
func (c *Center) invalidateDockerAvail() {
	c.availMu.Lock()
	c.availAt = time.Time{}
	c.availMu.Unlock()
}

func (c *Center) EnsureDocker() (string, error) {
	if c.DockerAvailable() {
		return "docker already available", nil
	}
	switch runtime.GOOS {
	case "darwin":
		// try brew cask
		if _, err := exec.LookPath("brew"); err == nil {
			cmd := procutil.Command("brew", "install", "--cask", "docker")
			out, err := cmd.CombinedOutput()
			return string(out), err
		}
		return "", fmt.Errorf("未检测到 Docker Desktop。请从 https://www.docker.com/products/docker-desktop/ 安装后重试")
	case "linux":
		// best-effort get.docker.com
		script := `curl -fsSL https://get.docker.com | sh`
		cmd := procutil.Command("bash", "-lc", script)
		out, err := cmd.CombinedOutput()
		return string(out), err
	case "windows":
		// 同步路径也必须走提权+静默，否则 WARP 页的「安装 Docker」按钮
		// 仍然是那个装不上的不提权 winget（两个入口行为不一致会很难查）。
		// 借一个临时任务承载日志，调用方只要最终结果。
		tmp := &PluginTask{Key: taskKeyDockerInstall}
		needReboot, err := c.installDockerWindows(tmp)
		if err != nil {
			return "", err
		}
		if needReboot {
			return "Docker Desktop 已安装，需要重启系统后生效", nil
		}
		return "Docker Desktop 安装完成", nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// EnsureDockerAsync 异步安装 Docker（macOS: brew cask / Windows: winget /
// Linux: 官方 get.docker.com 脚本），带阶段与流式日志，前端轮询 Task() 展示。
// 已安装则立即返回 ready 任务。安装后轮询等待守护进程就绪（Docker Desktop
// 首次启动需要时间），最多等 180 秒。
func (c *Center) EnsureDockerAsync() PluginTask {
	c.mu.Lock()
	if t := c.tasks[taskKeyDockerInstall]; t != nil && t.Running {
		c.mu.Unlock()
		return *t
	}
	t := &PluginTask{
		Key:     taskKeyDockerInstall,
		Running: true,
		Stage:   "detect",
		Message: "检测 Docker…",
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Started: time.Now(),
		Updated: time.Now(),
	}
	c.tasks[taskKeyDockerInstall] = t
	c.mu.Unlock()
	go c.runDockerInstall(t)
	return *t
}

func (c *Center) runDockerInstall(t *PluginTask) {
	defer func() {
		c.taskFinish(t.Key, t.OK, t.Stage, t.Message)
	}()

	if c.DockerAvailable() {
		t.Stage = "ready"
		t.Message = "Docker 已就绪（无需安装）"
		t.OK = true
		return
	}

	t.Stage = "install"
	// 0) 安装前清理重复下载：winget/brew/apt 每次都可能重新下载安装器，
	//    反复安装会在缓存目录堆出多份副本。保留最新一份，删掉其余。
	//    （三平台各自扫描对应缓存路径，见 findDockerInstallerCaches）
	if n, mb := cleanDockerInstallerCaches(true); n > 0 {
		c.taskLog(t.Key, fmt.Sprintf("已清理 %d 个重复的 Docker 安装文件（释放 %.0f MB），保留最新一份", n, mb))
	}

	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("brew"); err != nil {
			t.Stage = "failed"
			t.Message = "macOS 未检测到 Homebrew，无法全自动安装 Docker。请先安装 Homebrew（https://brew.sh）后重试"
			return
		}
		// 轻量无界面方案：colima（lima VM + Apple Virtualization.framework）。
		// 无 GUI 应用、无后台常驻大进程、brew 安装无需 sudo 密码，资源占用远小于 Docker Desktop。
		// 若已装 Docker Desktop（GUI 版，占资源），先自动卸载，统一切换到 colima。
		c.uninstallDockerDesktopQuiet(t)
		t.Message = "安装轻量无界面 Docker（colima 虚拟机 + docker CLI，无 GUI、低资源占用）…"
		c.taskLog(t.Key, "$ brew install colima docker")
		if err := c.runStreaming(t.Key, "brew", []string{"install", "colima", "docker"}, "", nil); err != nil {
			t.Stage = "failed"
			t.Message = "brew 安装 colima/docker 失败：" + err.Error() + "（可手动执行 brew install colima docker 后重试）"
			return
		}
		// colima 实例已存在时**绝不能**带规格 flag 启动：colima 的 --save-config
		// 默认 true，`colima start --cpu 2 --memory 4` 会把用户已调好的
		// 12C/16G 永久覆写成默认值（重启后生效）。只有全新实例才用默认规格创建。
		if c.colimaProfileExists() {
			t.Message = "启动 colima 虚拟机（已有实例，保留现有规格配置）…"
			c.taskLog(t.Key, "$ colima start")
			if err := c.runStreaming(t.Key, "colima", []string{"start"}, "", nil); err != nil {
				t.Stage = "failed"
				t.Message = "colima 启动失败：" + err.Error() + "（可手动执行 colima start 查看详细错误）"
				return
			}
		} else {
			t.Message = "创建并启动 colima 虚拟机（2 CPU / 4GB 内存 / 20GB 磁盘，Apple Virtualization 加速）…"
			c.taskLog(t.Key, "$ colima start --cpu 2 --memory 4 --disk 20 --vm-type vz")
			if err := c.runStreaming(t.Key, "colima", []string{"start", "--cpu", "2", "--memory", "4", "--disk", "20", "--vm-type", "vz"}, "", nil); err != nil {
				t.Stage = "failed"
				t.Message = "colima 启动失败：" + err.Error() + "（可手动执行 colima start 查看详细错误）"
				return
			}
		}
		c.taskLog(t.Key, "colima 虚拟机已启动，确认 docker 上下文…")
		// Docker Desktop 卸载后 ~/.docker/config.json 仍指向 docker-credential-desktop，
		// 会导致 docker pull 报 "executable file not found"。自动清理凭据助手引用。
		c.fixDockerCredsConfig(t)
	case "linux":
		t.Message = "运行 Docker 官方安装脚本（get.docker.com）…"
		c.taskLog(t.Key, "$ curl -fsSL https://get.docker.com | sh")
		if err := c.runStreaming(t.Key, "bash", []string{"-lc", "curl -fsSL https://get.docker.com | sh"}, "", nil); err != nil {
			t.Stage = "failed"
			t.Message = "Docker 官方脚本安装失败：" + err.Error()
			return
		}
	case "windows":
		// 1) 安装前清理重复下载：winget 每次都会重新下载安装器，反复安装会在
		//    缓存目录堆出多份副本（几十~几百 MB/份）。保留最新一份，删掉其余。
		if runtime.GOOS == "windows" {
			if n, mb := cleanDockerInstallerCaches(true); n > 0 {
				c.taskLog(t.Key, fmt.Sprintf("已清理 %d 个重复的 Docker 安装文件（释放 %.0f MB），保留最新一份", n, mb))
			}
		}

		// 2) 前置自愈：新电脑第一次跑会自动启用「虚拟机平台」功能、
		// 安装 WSL2 组件（一次 UAC）。启用后必须重启系统才生效，
		// 此时不能继续装 Docker（装了也起不来），停在「重启后继续」。
		if c.healWindowsPrereqs(t) {
			t.Stage = "ready"
			t.Message = "已启用 Windows 虚拟化组件（虚拟机平台 / WSL2），需要重启系统后生效。请重启电脑，回来后再次点击「安装 Docker」即可自动继续。"
			t.OK = true
			return
		}
		// 提权 + 静默 + winget 退出码归类，详见 docker_install_windows.go。
		// 旧实现是一行不提权的 winget install，在 Windows 上基本装不上。
		needReboot, werr := c.installDockerWindows(t)
		if werr != nil {
			t.Stage = "failed"
			t.Message = "安装 Docker Desktop 失败：" + werr.Error()
			return
		}
		if needReboot {
			// 待重启时不要去空等 180 秒守护进程 —— 它在重启前不可能起来，
			// 干等只会让用户以为安装挂了。
			t.Stage = "ready"
			t.Message = "Docker Desktop 已安装，需要重启系统后才能使用（WSL2/Hyper-V 组件待启用）。重启后回到本页即可一键部署打码引擎。"
			t.OK = true
			return
		}
		// 安装完成 → 自动启动 Docker Desktop。新装后它不会自启，
		// 旧实现直接掉进下面的 wait-daemon 干等 180 秒，必然超时报
		// 「守护进程未就绪」（实测：装完进程都没起来）。
		c.taskLog(t.Key, "启动 Docker Desktop（首次启动需初始化 WSL2 后端，约 1~3 分钟）…")
		if _, serr := c.startDockerDesktop(); serr != nil {
			t.Stage = "failed"
			t.Message = "Docker Desktop 已安装但启动失败：" + serr.Error()
			return
		}
		// startDockerDesktop 内部已等到守护进程就绪（240 秒窗口）
		c.invalidateDockerAvail()
		t.Stage = "ready"
		t.Message = "Docker 已就绪"
		t.OK = true
		c.taskLog(t.Key, "docker info 探测成功，守护进程就绪")
		return
	default:
		t.Stage = "failed"
		t.Message = fmt.Sprintf("暂不支持自动安装 Docker：%s/%s", runtime.GOOS, runtime.GOARCH)
		return
	}

	// 等待守护进程就绪（Docker Desktop 首次启动约 30~120s）
	t.Stage = "wait-daemon"
	t.Message = "等待 Docker 守护进程就绪（首次启动可能需要一两分钟）…"
	deadline := time.Now().Add(180 * time.Second)
	last := time.Now()
	for time.Now().Before(deadline) {
		// 这里必须绕过 DockerAvailable 的 2 秒缓存：本循环的唯一职责就是
		// 捕捉「不可用 → 可用」这次跳变，读缓存等于自己给自己加延迟。
		if c.probeDockerAvailable() {
			c.invalidateDockerAvail() // 让其它读者（Status/前端轮询）立刻看到新状态
			// 新装 Docker 后顺手清理 config.json 的 credsStore 残留
			// （卸载重装场景下它指向已删除的凭据助手，会让 pull 全失败）
			sanitizeDockerCredsStore()
			t.Stage = "ready"
			t.Message = "Docker 已就绪"
			t.OK = true
			c.taskLog(t.Key, "docker info 探测成功，守护进程就绪")
			return
		}
		if time.Since(last) >= 5*time.Second {
			c.taskLog(t.Key, "等待 Docker 守护进程…")
			last = time.Now()
		}
		time.Sleep(1 * time.Second)
	}
	t.Stage = "failed"
	t.Message = "Docker 已安装但守护进程未在 180 秒内就绪。请手动启动 Docker Desktop 后重试。"
}

// DeployDockerAsync 异步部署打码容器：拉取基础镜像（首次较慢）→ 清理旧容器 →
// docker run → 健康轮询（容器内首次安装 Chromium 可能需要数分钟）。
func (c *Center) DeployDockerAsync(req InstallRequest) (PluginTask, error) {
	key := deployTaskKey(req.ID)
	c.mu.Lock()
	if t := c.tasks[key]; t != nil && t.Running {
		c.mu.Unlock()
		return *t, nil
	}
	if !c.DockerAvailable() {
		c.mu.Unlock()
		// 区分「没装 Docker」和「装了但守护进程连不上」——后者在 colima 上很常见，
		// 提示去「安装 Docker」是把用户带向错误的操作。
		if c.dockerBinaryPresent() {
			return PluginTask{Key: key}, fmt.Errorf("%s", daemonDownHint())
		}
		return PluginTask{Key: key}, fmt.Errorf("未检测到 Docker，请先在插件中心完成 Docker 安装")
	}
	t := &PluginTask{
		Key:     key,
		Running: true,
		Stage:   "pull",
		Message: "准备部署打码容器…",
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Started: time.Now(),
		Updated: time.Now(),
	}
	c.tasks[key] = t
	c.mu.Unlock()
	go c.runDockerDeploy(t, req)
	return *t, nil
}

// DeployAllCaptchaDockerAsync 一键部署全部打码引擎容器（Auralith / VeloraTurn /
// EzSolver 三个一起）。幂等：已存在且为当前 bootstrap 版本的容器会被复用
// （秒级启动，Chromium 已内置），不会拆掉重装；某个引擎失败则任务停在失败处，
// 已就绪的引擎不受影响，重试会从失败的引擎继续。
func (c *Center) DeployAllCaptchaDockerAsync() (PluginTask, error) {
	c.mu.Lock()
	if t := c.tasks[taskKeyDeployAll]; t != nil && t.Running {
		c.mu.Unlock()
		return *t, nil
	}
	if !c.DockerAvailable() {
		c.mu.Unlock()
		// 区分「没装 Docker」和「装了但守护进程连不上」——后者在 colima 上很常见，
		// 提示去重装是把用户带向错误的操作。
		if c.dockerBinaryPresent() {
			return PluginTask{Key: taskKeyDeployAll}, fmt.Errorf("%s", daemonDownHint())
		}
		return PluginTask{Key: taskKeyDeployAll}, fmt.Errorf("未检测到 Docker，请先在「Docker 管理」安装 Docker")
	}
	t := &PluginTask{
		Key:     taskKeyDeployAll,
		Running: true,
		Stage:   "deploy",
		Message: "准备部署全部打码引擎容器…",
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Started: time.Now(),
		Updated: time.Now(),
	}
	c.tasks[taskKeyDeployAll] = t
	c.mu.Unlock()
	go c.runDeployAll(t)
	return *t, nil
}

// runDeployAll 部署一体容器：Auralith + VeloraTurn + EzSolver 装进同一个
// 容器（端口 8192/8193/8194 全部映射），切换通道只切端口、零重部署。
// 幂等：一体容器已存在且为当前 bootstrap 版本 → 复用（秒级启动，Chromium
// 已内置），不会拆掉重装；新建前自动移除旧的单引擎容器避免端口冲突。
func (c *Center) runDeployAll(t *PluginTask) {
	defer func() {
		c.taskFinish(t.Key, t.OK, t.Stage, t.Message)
	}()
	// 守护进程预检（见 runDockerDeployInner：宿主 socket 僵尸时绝不能往下走重建）
	if !c.daemonAlive() {
		t.Stage = "failed"
		t.Message = daemonDownHint()
		return
	}
	// Docker 卸载重装后 ~/.docker/config.json 可能残留指向已删除凭据助手
	// 的 credsStore，导致 docker pull 全部失败。部署前先清理（幂等，无副作用）。
	sanitizeDockerCredsStore()
	c.purgeCapturedCodeImages(t.Key)

	// 0) 幂等短路：一体容器已是当前 bootstrap 版本时，不该重新部署。
	//    旧实现无条件往下走 rm -f + 重建——用户点一次一键部署就把装好
	//    Chromium 的好容器拆了。
	switch st := c.containerState(combinedCaptchaContainer); {
	case st.DaemonDown:
		// 什么都探不到时绝不能往下走重建：那会 rm -f 掉一个可能完好的容器。
		t.Stage = "failed"
		t.Message = daemonDownHint()
		return
	case st.Exists && st.Bootstrap == dockerBootstrapVersion && !protectedRelease():
		started := st.Running
		if !started {
			if out, err := procutil.Command(dockerExecutable(), "start", combinedCaptchaContainer).CombinedOutput(); err == nil {
				started = true
			} else {
				c.taskLog(t.Key, "已有容器启动失败，转为重建："+strings.TrimSpace(string(out)))
			}
		}
		if started {
			if st.Running {
				c.taskLog(t.Key, "一体容器 "+combinedCaptchaContainer+" 已在运行，跳过重新部署")
			} else {
				c.taskLog(t.Key, "复用已有一体容器（Chromium 已就绪，秒级启动）")
			}
			// 复用路径不会重新执行 create，早先创建的容器可能仍是 restart=no，
			// Docker 引擎重启后不会自己回来。
			c.ensureRestartPolicy(combinedCaptchaContainer, t.Key)
			c.markAllDockerModes()
			c.awaitAllCaptchaHealthy(t)
			return
		}
	}

	// 1) 清理旧的单引擎容器（占着端口会挡映射，留着也会混淆状态判定）。
	for _, id := range captchaDeployOrder {
		old := c.containerName(id)
		if out, err := procutil.Command(dockerExecutable(), "rm", "-f", old).CombinedOutput(); err == nil {
			c.taskLog(t.Key, "已移除旧单引擎容器 "+old+"（并入一体容器）")
		} else if !strings.Contains(strings.ToLower(string(out)), "no such container") {
			c.taskLog(t.Key, "移除旧容器 "+old+" 失败（忽略，继续）："+trunc(string(out), 160))
		}
	}

	// 1.5) 停本地实例 + 端口清理：本机打码进程占着 8192/8193/8194 时，
	//      docker create 映射端口会 bind 失败（实测：AutoStart 起的本地
	//      实例 + 一键部署全部 = 必然失败「ports are not available」）。
	//      与单引擎部署路径（runDockerDeployInner）保持一致：先停登记过的
	//      本地进程，再按端口兜底杀残留（旧进程/僵尸 local 实例）。
	for _, id := range captchaDeployOrder {
		if cmd := c.takeProc(id); cmd != nil && cmd.Process != nil {
			c.taskLog(t.Key, "停止本地实例 "+string(id)+"（让出端口）…")
			_ = cmd.Process.Kill()
		}
	}
	for _, port := range []int{8192, 8193, 8194} {
		if n := c.killPortOwner(port); n > 0 {
			c.taskLog(t.Key, fmt.Sprintf("已清理 %d 个占用 %d 端口的残留进程", n, port))
		}
	}

	// 2) 基础镜像：优先一体固化镜像（Chromium 已内置），否则公共 python 镜像。
	image := c.combinedBaseImage()
	if image == capturedAllImageRef {
		c.taskLog(t.Key, "复用本地已固化一体镜像 "+image+"（Chromium 已内置，跳过容器内 apt）")
	} else {
		t.Stage = "pull"
		t.Message = "拉取基础镜像 " + image + "（首次需下载）…"
		c.taskLog(t.Key, "$ docker pull "+image)
		if err := c.runStreaming(t.Key, dockerExecutable(), []string{"pull", image}, "", nil); err != nil {
			if procutil.Command(dockerExecutable(), "image", "inspect", image).Run() == nil {
				c.taskLog(t.Key, "拉取失败但镜像已存在（网络瞬时问题），跳过继续部署…")
			} else {
				t.Stage = "failed"
				t.Message = "拉取镜像失败：" + c.wrapDockerErr(err).Error()
				return
			}
		}
	}

	// 3) 端口清理：本机残留进程（旧 local 实例/僵尸进程）占着 8192/8193/8194
	//    会挡容器端口映射。
	for _, p := range []int{8192, 8193, 8194} {
		if n := c.killPortOwner(p); n > 0 {
			c.taskLog(t.Key, fmt.Sprintf("已清理 %d 个占用 %d 端口的残留进程", n, p))
		}
	}

	// 4) 建容器并送文件。
	t.Stage = "deploy"
	t.Message = "创建一体容器 " + combinedCaptchaContainer + " …"
	c.taskLog(t.Key, fmt.Sprintf("Docker 引擎架构=linux/%s（三个引擎二进制按此挑选，与宿主 %s 无关）",
		c.dockerEngineArch(), runtime.GOARCH))
	c.taskLog(t.Key, "创建一体容器（Auralith + VeloraTurn + EzSolver 装进同一个容器，端口 8192/8193/8194）…")
	if err := c.createCombinedContainer(image, func(s string) { c.taskLog(t.Key, s) }); err != nil {
		t.Stage = "failed"
		t.Message = c.wrapDockerErr(err).Error()
		return
	}
	c.markAllDockerModes()

	// 5) 早死快检：架构不符 / 文件不可执行会在秒级 exit 126|127，
	//    不用等满 10 分钟健康轮询。
	for i := 0; i < 4; i++ {
		time.Sleep(1500 * time.Millisecond)
		if dead, code, tail := c.containerDied(combinedCaptchaContainer); dead {
			t.Stage = "failed"
			t.Message = fmt.Sprintf("容器启动后立即退出（exit %d）。", code)
			if code == 126 || code == 127 {
				t.Message += fmt.Sprintf("通常是二进制架构不符或不可执行——引擎为 linux/%s，"+
					"请确认 plugins/ 下有对应架构的二进制。", c.dockerEngineArch())
			}
			if tail != "" {
				c.taskLog(t.Key, "容器日志: "+tail)
				t.Message += " 日志尾部：" + trunc(tail, 300)
			}
			return
		}
	}

	// 6) 健康轮询（首次容器内 apt 装 Chromium + Xvfb，放宽到 10 分钟）
	c.awaitAllCaptchaHealthy(t)
}

func (c *Center) runDockerDeploy(t *PluginTask, req InstallRequest) {
	defer func() {
		c.taskFinish(t.Key, t.OK, t.Stage, t.Message)
	}()
	c.runDockerDeployInner(t, req)
}

// runDockerDeployInner 是单个打码引擎的容器部署核心（模式切换 → 幂等短路 →
// 拉镜像 → 端口清理 → 建容器 → 早死快检 → 健康轮询）。
// 单引擎部署与「一键部署全部」共用；批量部署时多个引擎顺序执行，
// 任务完成状态（taskFinish）由外层统一收口。
func (c *Center) runDockerDeployInner(t *PluginTask, req InstallRequest) {
	// 切换 docker 模式状态（Status() 依赖它做容器优先判定，避免 local 假阳性）
	c.setMode(req.ID, ModeDocker)

	name := c.containerName(req.ID)
	port := captchaPort(req.ID)
	if req.Port > 0 {
		port = req.Port
	}
	// 0) 守护进程预检。colima 有个常见故障：宿主侧转发 socket 变僵尸，
	//    而 VM 仍在跑 —— colima list 显示 Running、colima ssh 也通，
	//    但所有 docker 命令都报 "Cannot connect to the Docker daemon"。
	//    先探一次，掉线就直接给可操作提示，不要跑到一半再抛 CLI 原文。
	if !c.daemonAlive() {
		t.Stage = "failed"
		t.Message = daemonDownHint()
		c.taskLog(t.Key, t.Message)
		return
	}
	c.purgeCapturedCodeImages(t.Key)

	// 0.5) 幂等短路：容器已经是当前 bootstrap 版本时，切到 docker 模式不该重新部署。
	//      旧实现无条件往下走 rm -f + 重建——用户在早就部署好的插件上点一次
	//      Docker，就把好容器连同已固化的 Chromium 环境一起拆了。
	switch st := c.containerState(name); {
	case st.DaemonDown:
		// 什么都探不到时绝不能往下走重建：那会 rm -f 掉一个可能完好的容器。
		t.Stage = "failed"
		t.Message = daemonDownHint()
		c.taskLog(t.Key, t.Message)
		return
	case st.Exists && st.Bootstrap == dockerBootstrapVersion && !protectedRelease():
		started := st.Running
		if !started {
			if out, err := procutil.Command(dockerExecutable(), "start", name).CombinedOutput(); err == nil {
				started = true
			} else {
				c.taskLog(t.Key, "已有容器启动失败，转为重建："+strings.TrimSpace(string(out)))
			}
		}
		if started {
			if st.Running {
				c.taskLog(t.Key, "容器 "+name+" 已在运行且为当前版本，跳过重新部署")
			} else {
				c.taskLog(t.Key, "复用已有容器 "+name+"（Chromium 已就绪，秒级启动）")
			}
			// 复用路径不会重新执行 create，早先创建的容器可能仍是 restart=no，
			// Docker 引擎重启后不会自己回来。
			c.ensureRestartPolicy(name, t.Key)
			c.setDockerName(req.ID, name)
			c.awaitHealthy(t, req.ID, name, port)
			return
		}
	}

	image := c.baseImage(req.ID)

	// 1) pull（首次下载镜像；网络瞬时失败但本地已有镜像时继续）。
	//    已固化的本地镜像不在任何 registry 上，不能去 pull。
	if image == capturedImage(req.ID) {
		c.taskLog(t.Key, "复用本地已固化镜像 "+image+"（Chromium 已内置，跳过容器内 apt）")
	} else {
		t.Stage = "pull"
		t.Message = "拉取基础镜像 " + image + "（首次需下载）…"
		c.taskLog(t.Key, "$ docker pull "+image)
		if err := c.runStreaming(t.Key, dockerExecutable(), []string{"pull", image}, "", nil); err != nil {
			if procutil.Command(dockerExecutable(), "image", "inspect", image).Run() == nil {
				c.taskLog(t.Key, "拉取失败但镜像已存在（网络瞬时问题），跳过继续部署…")
			} else {
				t.Stage = "failed"
				t.Message = "拉取镜像失败：" + c.wrapDockerErr(err).Error()
				return
			}
		}
	}

	// 2) 停 local + 端口清理。走到这里说明 0.5 的幂等短路没有命中，
	//    确定要新建容器了（真正的 rm -f 在 createContainerWithPayload 里）。
	t.Stage = "deploy"
	t.Message = "清理旧容器并启动 " + name + " …"
	if cmd := c.takeProc(req.ID); cmd != nil && cmd.Process != nil {
		c.taskLog(t.Key, "停止本地实例（让出端口 "+fmt.Sprint(port)+"）…")
		_ = cmd.Process.Kill()
	}
	// 兜底：按端口杀残留（旧进程/僵尸 local 实例也可能占着端口）
	if n := c.killPortOwner(port); n > 0 {
		c.taskLog(t.Key, fmt.Sprintf("已清理 %d 个占用 %d 端口的残留进程", n, port))
	}

	// 3) 走到这里说明没有可复用容器（0.5 的幂等短路已处理复用），新建。
	c.taskLog(t.Key, fmt.Sprintf("Docker 引擎架构=linux/%s（容器二进制按此挑选，与宿主 %s 无关）",
		c.dockerEngineArch(), runtime.GOARCH))
	if image == capturedImage(req.ID) {
		c.taskLog(t.Key, "创建新容器（用已固化镜像，Chromium 已就绪，秒级启动）…")
	} else {
		c.taskLog(t.Key, "创建新容器（首次需在容器内安装 Chromium，约 3~8 分钟）…")
	}
	if err := c.createContainerWithPayload(req.ID, name, port, image, func(s string) { c.taskLog(t.Key, s) }); err != nil {
		t.Stage = "failed"
		t.Message = c.wrapDockerErr(err).Error()
		return
	}
	c.setDockerName(req.ID, name)

	// 3.5) 早死快检：架构不符 / 文件不可执行会在秒级 exit 126|127。
	// 旧实现直接进 10 分钟健康轮询，用户要等满 10 分钟才看到失败。
	for i := 0; i < 4; i++ {
		time.Sleep(1500 * time.Millisecond)
		if dead, code, tail := c.containerDied(name); dead {
			t.Stage = "failed"
			t.Message = fmt.Sprintf("容器启动后立即退出（exit %d）。", code)
			if code == 126 || code == 127 {
				t.Message += fmt.Sprintf("通常是二进制架构不符或不可执行——引擎为 linux/%s，"+
					"请确认 plugins/ 下有对应架构的二进制。", c.dockerEngineArch())
			}
			if tail != "" {
				c.taskLog(t.Key, "容器日志: "+tail)
				t.Message += " 日志尾部：" + trunc(tail, 300)
			}
			return
		}
	}

	// 4) 健康轮询（容器内首次要 apt 装 Chromium + Xvfb，放宽到 10 分钟）
	c.awaitHealthy(t, req.ID, name, port)
}

// awaitHealthy 轮询等待打码服务就绪。复用路径和新建路径都要走它，
// 所以抽出来，避免复用时另写一份轮询逻辑。
func (c *Center) awaitHealthy(t *PluginTask, id PluginID, name string, port int) {
	t.Stage = "health"
	t.Message = "等待打码服务就绪（容器内首次安装 Chromium/Xvfb，可能需要几分钟）…"
	deadline := time.Now().Add(600 * time.Second)
	last := time.Now()
	for time.Now().Before(deadline) {
		st := c.Status(id)
		if st.Healthy {
			t.Stage = "ready"
			t.Message = "打码服务已就绪 · http://127.0.0.1:" + fmt.Sprint(port) + "/health"
			t.OK = true
			c.taskLog(t.Key, "容器健康检查通过，打码服务运行中")
			// 固化镜像：把已装好 Chromium/Xvfb 的容器 commit 成本地镜像，
			// 下次部署直接用它，省掉 3~8 分钟 apt，也大幅缩短
			// 「长时间 apt 期间守护进程掉线」的暴露窗口。best-effort，失败只记日志。
			c.captureImage(id, name, t.Key)
			return
		}
		if time.Since(last) >= 10*time.Second {
			c.taskLog(t.Key, "健康检查未通过，继续等待…（容器日志尾部见下）")
			tail := c.containerLogTail(name)
			if tail != "" {
				c.taskLog(t.Key, "容器日志: "+tail)
			}
			last = time.Now()
		}
		time.Sleep(3 * time.Second)
	}
	tail := c.containerLogTail(name)
	t.Stage = "failed"
	t.Message = "打码容器在 10 分钟内未就绪。"
	if tail != "" {
		t.Message += " 容器日志尾部：" + trunc(tail, 400)
	}
}

// ensureRestartPolicy 把已存在容器的重启策略补成 unless-stopped。
// docker update 可以在线改，不需要重建容器。
func (c *Center) ensureRestartPolicy(name, taskKey string) {
	out, err := procutil.Command(dockerExecutable(), "inspect",
		"--format", "{{.HostConfig.RestartPolicy.Name}}", name).Output()
	if err == nil && strings.TrimSpace(string(out)) == "unless-stopped" {
		return
	}
	if uo, uerr := procutil.Command(dockerExecutable(), "update",
		"--restart", "unless-stopped", name).CombinedOutput(); uerr != nil {
		if taskKey != "" {
			c.taskLog(taskKey, "补齐重启策略失败（Docker 重启后需手动拉起）："+trunc(string(uo), 160))
		}
		return
	}
	if taskKey != "" {
		c.taskLog(taskKey, "已补齐重启策略 unless-stopped：Docker 引擎重启后容器会自动回来")
	}
}

// purgeCapturedCodeImages removes legacy tags created by older releases that
// committed live execution containers (and therefore plaintext protected code).
// A running container can keep its layers until it is replaced, but protected
// deployment never reuses or creates these tags again.
func (c *Center) purgeCapturedCodeImages(taskKey string) {
	if !protectedRelease() {
		return
	}
	refs := []string{capturedAllImageRef}
	for _, id := range captchaDeployOrder {
		refs = append(refs, capturedImage(id))
	}
	for _, ref := range refs {
		if !c.imageExists(ref) {
			continue
		}
		out, err := procutil.Command(dockerExecutable(), "image", "rm", "-f", ref).CombinedOutput()
		if err != nil {
			c.taskLog(taskKey, "清理旧代码镜像失败 "+ref+"："+trunc(string(out), 160))
			continue
		}
		c.taskLog(taskKey, "已移除旧代码镜像标签 "+ref)
	}
}

// captureImage 把 bootstrap 完成的容器 commit 成本地镜像供下次复用。
// 已存在则跳过。失败不影响本次部署（只是下次还得再 apt 一遍）。
func (c *Center) captureImage(id PluginID, container, taskKey string) {
	if protectedRelease() {
		c.taskLog(taskKey, "受保护发布不固化执行容器：避免明文打码核心进入可复用 Docker 镜像")
		return
	}
	ref := capturedImage(id)
	if c.imageExists(ref) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "commit", container, ref).CombinedOutput()
	if err != nil {
		c.taskLog(taskKey, "固化镜像失败（不影响本次部署，下次仍会重新装 Chromium）："+
			trunc(string(out), 200))
		return
	}
	c.taskLog(taskKey, "已固化镜像 "+ref+"：下次部署可跳过容器内 Chromium 安装")
}

func (c *Center) containerLogTail(name string) string {
	out, err := procutil.Command(dockerExecutable(), "logs", "--tail", "20", name).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runBrewCaskDocker 在 pty 里运行 brew install --cask docker：
// macOS 的 script 命令分配 pty，让 brew 内部的 sudo 能交互；遇到密码提示时
// 弹系统授权框（osascript display dialog，隐藏输入）取得密码后写入 sudo stdin，
// 密码只存在于内存，不落盘、不进日志。
// 注意：sudo 的 "Password:" 提示不带换行，必须按字节流累积检测尾部关键字，
// 按行扫描（bufio.Scanner）会永远等不到这一行。
func (c *Center) runBrewCaskDocker(t *PluginTask) error {
	cmd := procutil.Command("script", "-q", "/dev/null", "brew", "install", "--cask", "docker")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	passwordAsked := false
	go func() {
		buf := make([]byte, 4096)
		lineBuf := make([]byte, 0, 4096)
		ask := func(tail string) {
			if passwordAsked {
				return
			}
			low := strings.ToLower(tail)
			hit := strings.Contains(low, "password") && (strings.Contains(low, ":") || strings.Contains(low, "for "))
			if !hit && strings.Contains(low, "密码") {
				hit = true
			}
			if !hit {
				return
			}
			passwordAsked = true
			c.taskLog(t.Key, "需要管理员密码：请在系统授权框输入（仅本次安装使用，不会保存）…")
			pwd := c.promptAdminPassword()
			if pwd == "" {
				c.taskLog(t.Key, "授权已取消")
				_ = stdin.Close()
				return
			}
			_, _ = stdin.Write([]byte(pwd + "\n"))
			c.taskLog(t.Key, "已提交密码，继续安装…")
		}
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				lineBuf = append(lineBuf, buf[:n]...)
				for {
					idx := bytes.IndexByte(lineBuf, '\n')
					if idx < 0 {
						break
					}
					line := strings.TrimRight(string(lineBuf[:idx]), "\r")
					lineBuf = lineBuf[idx+1:]
					c.taskLog(t.Key, line)
					ask(line)
				}
				// 无换行的尾部：sudo 密码提示正等在这里
				if len(lineBuf) > 0 {
					ask(string(lineBuf))
				}
			}
			if rerr != nil {
				if len(lineBuf) > 0 {
					c.taskLog(t.Key, strings.TrimRight(string(lineBuf), "\r"))
				}
				break
			}
		}
	}()
	werr := cmd.Wait()
	_ = stdin.Close()
	return werr
}

// promptAdminPassword 弹 macOS 原生密码框（隐藏输入），返回密码或空串（取消）。
func (c *Center) promptAdminPassword() string {
	apple := `display dialog "UmbraForge 正在安装 Docker Desktop，需要管理员密码（仅本次安装使用，不会保存）" default answer "" with hidden answer with icon caution buttons {"取消", "确定"} default button 2`
	out, err := procutil.Command("osascript", "-e", apple, "-e", "text returned of result").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// removeFileAdmin 用 macOS 管理员授权（sudo）删除文件；用户取消返回 false。
func (c *Center) removeFileAdmin(path string, t *PluginTask) bool {
	c.taskLog(t.Key, "弹管理员授权清理 "+path+" …")
	script := fmt.Sprintf(`do shell script "rm -f '%s'" with administrator privileges`, strings.ReplaceAll(path, "'", `\'`))
	if err := procutil.Command("osascript", "-e", script).Run(); err != nil {
		c.taskLog(t.Key, "管理员授权清理被取消或失败："+err.Error())
		return false
	}
	c.taskLog(t.Key, "管理员授权清理成功")
	return true
}

// killPortOwner 杀掉监听指定端口的进程（macOS/Linux 用 lsof），返回杀掉的进程数。
// 用于 Docker 部署前清理残留 local 打码实例，让端口归容器。
// 绝不能碰 Docker/lima/colima 的运行时和端口转发进程：容器 -p 映射在宿主侧
// 就是它们监听的，误杀会连带打掉同一 lima ssh 主进程上其它插件的端口转发。
func (c *Center) killPortOwner(port int) int {
	if runtime.GOOS == "windows" {
		return c.killPortOwnerWindows(port)
	}
	out, err := procutil.Command("lsof", "-tiTCP:"+fmt.Sprint(port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range strings.Fields(string(out)) {
		if f == "" || f == fmt.Sprint(os.Getpid()) {
			continue
		}
		if comm, err := procutil.Command("ps", "-o", "comm=", "-p", f).Output(); err == nil && isDockerInfraProcess(string(comm)) {
			continue // 命中 Docker/lima/colima 基础设施白名单，不杀也不计数
		}
		if procutil.Command("kill", f).Run() == nil {
			n++
		}
	}
	return n
}

// killPortOwnerWindows 杀掉监听指定端口的进程（Windows 版）。
//
// 旧实现直接 return 0（「Windows 用 c.procs 管理，跳过」）—— 这个假设在
// **孤儿进程**场景下是错的：app 重启后上一代的打码子进程不会跟着死
// （Stop-Process 只杀父进程），它们继续占着 8192/8193/8194，c.procs 里
// 又没有登记，于是 Docker 部署端口映射 bind 失败（实测踩坑）。
//
// 用 Get-NetTCPConnection 找监听者。绝不能碰 Docker 基础设施：容器 -p 映射
// 在宿主侧就是 com.docker.backend / vpnkit / wslrelay 在监听，误杀会打掉
// Docker 运行时 —— 按进程名做白名单过滤（与 Linux 的 isDockerInfraProcess
// 同一意图）。返回杀掉的进程数。
func (c *Center) killPortOwnerWindows(port int) int {
	ps := fmt.Sprintf(
		`$killed = 0; `+
			`Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue | `+
			`ForEach-Object { `+
			`$p = Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue; `+
			`if ($p -and $p.ProcessName -notmatch 'docker|vpnkit|wslrelay|wsl|com\.docker|vmmem') { `+
			`Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue; $killed++ } }; `+
			`Write-Output $killed`, port)
	out, err := procutil.Command("powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", ps).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// fixDockerCredsConfig 清理 ~/.docker/config.json 里指向 Docker Desktop 的
// 凭据助手引用（credsStore / credHelpers），否则切换 colima 后 docker pull
// 会报 docker-credential-desktop not found。原文件备份为 .umbraforge-bak。
func (c *Center) fixDockerCredsConfig(t *PluginTask) {
	cfgPath := filepath.Join(os.Getenv("HOME"), ".docker", "config.json")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return // 无配置文件，无需处理
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return
	}
	changed := false
	if _, ok := m["credsStore"]; ok {
		delete(m, "credsStore")
		changed = true
	}
	if ch, ok := m["credHelpers"].(map[string]any); ok && len(ch) > 0 {
		delete(m, "credHelpers")
		changed = true
	}
	if !changed {
		return
	}
	_ = os.Rename(cfgPath, cfgPath+".umbraforge-bak")
	if out, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(cfgPath, out, 0o600)
		c.taskLog(t.Key, "已清理 ~/.docker/config.json 中的 Docker Desktop 凭据助手引用（原文件备份为 config.json.umbraforge-bak）")
	}
}

// uninstallDockerDesktopQuiet 静默卸载 GUI 版 Docker Desktop（macOS）。
// colima 方案不需要它，且它占资源；清理后统一切换到 colima。
// 仅删应用本体与退出进程，不动用户数据（~/.docker 等）。
func (c *Center) uninstallDockerDesktopQuiet(t *PluginTask) {
	if runtime.GOOS != "darwin" {
		return
	}
	removed := false
	for _, app := range []string{"/Applications/Docker.app", "/Applications/Docker Desktop.app"} {
		if st, err := os.Stat(app); err == nil && st.IsDir() {
			_ = procutil.Command("osascript", "-e", `quit app "Docker"`).Run()
			_ = procutil.Command("osascript", "-e", `quit app "Docker Desktop"`).Run()
			time.Sleep(2 * time.Second)
			if err := os.RemoveAll(app); err != nil {
				c.taskLog(t.Key, "卸载 Docker Desktop 失败（"+app+"）: "+err.Error())
			} else {
				c.taskLog(t.Key, "已卸载 GUI 版 Docker Desktop（"+app+"），改用轻量 colima")
				removed = true
			}
		}
	}
	if removed {
		// 清理 Desktop 的 launchd/登录项残留，避免下次开机又拉起
		for _, agent := range []string{
			filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.docker.docker.plist"),
			filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.docker.helper.plist"),
		} {
			_ = os.Remove(agent)
		}
	}
}

// stageDockerPayload 把要送进容器的插件文件集中复制到数据目录
// （~/Library/Application Support/UmbraForge/docker-mounts/<plugin>-<arch>/），
// 返回该暂存目录。文件由 docker cp 经守护进程 API 送入容器（不再用 -v 绑定挂载，
// 详见 docker_payload.go 顶部注释：挂载源在 VM 里不可见时 Docker 会静默建空目录）。
//
// 二进制按 arch 挑选——容器架构由 Docker 引擎决定（Apple Silicon + colima =
// linux/arm64），固定用 amd64 会直接 exec 失败。暂存目录带 arch 后缀，
// 换引擎架构时不会复用上一次的错架构文件。已存在且大小一致则跳过复制。
func (c *Center) stageDockerPayload(id PluginID, arch string) (dst string, err error) {
	stageBase := filepath.Join(c.mutableStateRoot(), "docker-staging")
	if err := os.MkdirAll(stageBase, 0o700); err != nil {
		return "", err
	}
	dst, err = os.MkdirTemp(stageBase, string(id)+"-"+arch+"-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dst)
		}
	}()
	copyFile := func(src, name string) error {
		if !fileExists(src) {
			return fmt.Errorf("缺少挂载源文件 %s", src)
		}
		out := filepath.Join(dst, name)
		if si, err := os.Stat(out); err == nil {
			if s2, err2 := os.Stat(src); err2 == nil && si.Size() == s2.Size() {
				// 已复制且大小一致：旧文件可能是 0644（早期 bug），强制修正执行位
				_ = os.Chmod(out, 0o755)
				return nil
			}
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		tmp := out + ".tmp"
		o, err := os.Create(tmp)
		if err != nil {
			return err
		}
		if _, err := io.Copy(o, in); err != nil {
			o.Close()
			return err
		}
		if err := o.Close(); err != nil {
			return err
		}
		// 二进制需要可执行位（容器内 :ro 挂载无法 chmod，权限必须在 host 侧就位）
		_ = os.Chmod(tmp, 0o755)
		return os.Rename(tmp, out)
	}
	switch id {
	case PluginEzSolver:
		base := filepath.Join(c.root, "plugins", "ezsolver")
		for _, f := range []string{"service.py", "solver.py"} {
			if err := copyFile(filepath.Join(base, f), f); err != nil {
				return "", err
			}
		}
	case PluginVeloraTurn, PluginAuralith:
		dirName := map[PluginID]string{PluginVeloraTurn: "veloraturn", PluginAuralith: "auralith"}[id]
		destName := map[PluginID]string{PluginVeloraTurn: "veloraturn", PluginAuralith: "auralithd"}[id]
		base := filepath.Join(c.root, "plugins", dirName)
		copied := false
		for _, name := range linuxPluginBinary(id, arch) {
			if src := filepath.Join(base, name); fileExists(src) {
				if err := copyFile(src, destName); err != nil {
					return "", err
				}
				copied = true
				break
			}
		}
		if !copied {
			return "", fmt.Errorf("%s: 找不到 linux/%s 二进制（plugins/%s/ 下应有 %s）。"+
				"Docker 引擎架构为 %s，架构不符的二进制在容器里无法执行",
				id, arch, dirName, strings.Join(linuxPluginBinary(id, arch), " 或 "), arch)
		}
	}
	keep = true
	return dst, nil
}

// brewCaskConflictPath 从最近的 brew 安装日志里提取
// "already a Binary at '<path>'" 冲突路径（旧版残留二进制阻挡 cask 安装）。
func (c *Center) brewCaskConflictPath() string {
	for _, l := range c.Task(taskKeyDockerInstall).Log {
		marker := "already a Binary at '"
		if i := strings.Index(l, marker); i >= 0 {
			rest := l[i+len(marker):]
			if j := strings.Index(rest, "'"); j > 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// UninstallDocker removes UmbraForge-managed WARP containers then attempts to uninstall Docker.
// Scope is best-effort and OS-specific. Does not wipe unrelated user containers unless remove_all_containers=true.
func (c *Center) UninstallDocker(removeAllContainers bool) (map[string]any, error) {
	out := map[string]any{
		"os":    runtime.GOOS,
		"arch":  runtime.GOARCH,
		"steps": []string{},
	}
	steps := []string{}
	add := func(s string) { steps = append(steps, s) }

	dockerBin, err := exec.LookPath(dockerExecutable())
	hasDocker := err == nil
	out["docker_path"] = dockerBin
	out["docker_present"] = hasDocker

	if hasDocker {
		// always clean umbra warp instances first
		cmd := procutil.Command(dockerExecutable(), "ps", "-aq", "--filter", "name=umbra-warp")
		b, _ := cmd.CombinedOutput()
		ids := strings.Fields(strings.TrimSpace(string(b)))
		if len(ids) > 0 {
			_ = procutil.Command(dockerExecutable(), append([]string{"rm", "-f"}, ids...)...).Run()
			add(fmt.Sprintf("removed %d umbra-warp container(s)", len(ids)))
		} else {
			add("no umbra-warp containers")
		}
		// remove known warp images
		for _, img := range []string{"monius/docker-warp-socks:latest", "dublok/cloudflare-warp:latest"} {
			if procutil.Command(dockerExecutable(), "image", "inspect", img).Run() == nil {
				_ = procutil.Command(dockerExecutable(), "rmi", "-f", img).Run()
				add("removed image " + img)
			}
		}
		if removeAllContainers {
			all, _ := procutil.Command(dockerExecutable(), "ps", "-aq").CombinedOutput()
			allIDs := strings.Fields(strings.TrimSpace(string(all)))
			if len(allIDs) > 0 {
				_ = procutil.Command(dockerExecutable(), append([]string{"rm", "-f"}, allIDs...)...).Run()
				add(fmt.Sprintf("removed all containers: %d", len(allIDs)))
			}
		}
	} else {
		add("docker binary not found; skip container cleanup")
	}

	switch runtime.GOOS {
	case "darwin":
		// quit Docker Desktop
		_ = procutil.Command("osascript", "-e", `quit app "Docker"`).Run()
		_ = procutil.Command("osascript", "-e", `quit app "Docker Desktop"`).Run()
		time.Sleep(2 * time.Second)
		// remove app bundles
		for _, app := range []string{"/Applications/Docker.app", "/Applications/Docker Desktop.app"} {
			if st, err := os.Stat(app); err == nil && st.IsDir() {
				if err := os.RemoveAll(app); err != nil {
					add("failed remove " + app + ": " + err.Error())
				} else {
					add("removed " + app)
				}
			}
		}
		// optional CLI leftovers
		for _, p := range []string{"/usr/local/bin/docker", "/usr/local/bin/docker-compose", "/usr/local/bin/kubectl.docker"} {
			if _, err := os.Stat(p); err == nil {
				_ = os.Remove(p)
				add("removed " + p)
			}
		}
		out["hint"] = "已尝试卸载 Docker Desktop。若仍提示残留，请到「应用程序」手动删除 Docker，并检查登录项。"
	case "windows":
		// winget uninstall
		if _, err := exec.LookPath("winget"); err == nil {
			cmd := procutil.Command("winget", "uninstall", "-e", "--id", "Docker.DockerDesktop", "--accept-source-agreements")
			b, err := cmd.CombinedOutput()
			add(strings.TrimSpace(string(b)))
			if err != nil {
				out["steps"] = steps
				return out, fmt.Errorf("winget uninstall failed: %v", err)
			}
		} else {
			out["steps"] = steps
			return out, fmt.Errorf("未找到 winget，请手动卸载 Docker Desktop")
		}
		out["hint"] = "已通过 winget 卸载 Docker Desktop"
	case "linux":
		// stop service, do not force purge packages without package manager detection
		_ = procutil.Command("systemctl", "stop", "docker").Run()
		_ = procutil.Command("systemctl", "disable", "docker").Run()
		add("stopped/disabled docker service (if present)")
		// try common package managers (best effort)
		if _, err := exec.LookPath("apt-get"); err == nil {
			cmd := procutil.Command("bash", "-lc", "sudo -n apt-get remove -y docker-ce docker-ce-cli containerd.io docker-compose-plugin 2>/dev/null || apt-get remove -y docker.io docker-compose 2>/dev/null || true")
			b, _ := cmd.CombinedOutput()
			add(strings.TrimSpace(string(b)))
		} else if _, err := exec.LookPath("dnf"); err == nil {
			cmd := procutil.Command("bash", "-lc", "sudo -n dnf remove -y docker-ce docker-ce-cli containerd.io 2>/dev/null || true")
			b, _ := cmd.CombinedOutput()
			add(strings.TrimSpace(string(b)))
		} else {
			add("no supported package manager auto-remove; stop service only")
		}
		out["hint"] = "Linux 已尝试停止服务并移除 docker 包；若需完全清理请按发行版文档处理 residual 配置"
	default:
		out["steps"] = steps
		return out, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	// re-check
	still := false
	if _, err := exec.LookPath(dockerExecutable()); err == nil {
		still = procutil.Command(dockerExecutable(), "info").Run() == nil
	}
	out["docker_still_running"] = still
	// 卸载成功后扫描残留的安装器下载缓存，交给前端弹窗询问是否删除
	// （三平台各自扫对应缓存路径，见 findDockerInstallerCaches）
	out["installer_files"] = findDockerInstallerCaches()
	out["steps"] = steps
	if still {
		return out, fmt.Errorf("Docker 仍可运行，可能需要手动完成卸载或重启后重试")
	}
	return out, nil
}

func (c *Center) Status(id PluginID) PluginStatus {
	port := captchaPort(id)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	// 模式取 effectiveMode 而不是裸 mode()：states 是纯内存表且重启后一律回落
	// ModeLocal，直接用它会把 Docker 部署好的引擎报成「本机运行」（用户看到的
	// 「部署好的打码没被识别」就是这个）。见 docker_combined.go:effectiveMode。
	mode := c.effectiveMode(id)
	st := PluginStatus{
		ID: id, Mode: mode, URL: url,
		OS: runtime.GOOS, Arch: runtime.GOARCH, DockerOK: c.DockerAvailable(),
	}
	// Docker 模式：容器没在跑时，端口响应可能是 local 实例占着（假阳性），
	// 必须以容器状态为准。一体容器（umbraforge-captcha）与旧单引擎容器名都算。
	if mode == ModeDocker {
		if c.DockerAvailable() {
			if c.dockerRunningCaptcha(id) == "" {
				st.Installed = true
				st.Running = false
				st.Healthy = false
				st.Message = "docker 容器未运行（端口可能被本地实例占用，请先停止本地模式）"
				return st
			}
		} else {
			st.Message = "docker 不可用"
			return st
		}
	}
	// health
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/health")
	if err == nil {
		defer resp.Body.Close()
		st.Healthy = resp.StatusCode >= 200 && resp.StatusCode < 500
		st.Running = st.Healthy
		st.Installed = true
		st.Message = fmt.Sprintf("http %d", resp.StatusCode)
		// auralith 额外检查浏览器就绪：容器活着但 Chrome 起不来时引擎会返回
		// 503 worker queue timeout（Windows Docker/WSL2 常见）——端口通但 worker
		// 全不可用，必须判不健康，EnsureCaptchaReady 才能回落到本机模式。
		if id == PluginAuralith && st.Healthy {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			var h struct {
				BrowserReady *bool `json:"browser_ready"`
				Browsers     int   `json:"browsers"`
			}
			if json.Unmarshal(body, &h) == nil {
				if h.BrowserReady != nil && !*h.BrowserReady {
					st.Healthy = false
					st.Running = false
					st.Message = "引擎已响应但浏览器未就绪（browser_ready=false，Chrome 起不来）"
					return st
				}
				if h.BrowserReady == nil && h.Browsers == 0 {
					st.Healthy = false
					st.Running = false
					st.Message = "引擎已响应但无可用浏览器 worker（browsers=0）"
					return st
				}
			}
		}
		return st
	}
	// docker ps
	if c.DockerAvailable() {
		if name := c.dockerRunningCaptcha(id); name != "" {
			st.Running = true
			st.Installed = true
			st.Mode = ModeDocker
			st.Message = "docker container present; health endpoint not ready"
			return st
		}
	}
	// local path markers
	switch id {
	case PluginEzSolver:
		st.Installed = fileExists(filepath.Join(c.root, "plugins", "ezsolver", "service.py"))
	case PluginVeloraTurn:
		st.Installed = fileExists(filepath.Join(c.root, "plugins", "veloraturn")) || fileExists(filepath.Join(c.root, "plugins", "veloraturn", "src"))
	case PluginAuralith:
		st.Installed = fileExists(filepath.Join(c.root, "plugins", "auralith", "install-local.sh"))
	}
	st.Message = err.Error()
	return st
}

func (c *Center) ListStatus() []PluginStatus {
	out := make([]PluginStatus, 0, 3)
	for _, p := range c.Catalog() {
		out = append(out, c.Status(p.ID))
	}
	return out
}

func (c *Center) containerName(id PluginID) string {
	return "umbraforge-" + string(id)
}

func (c *Center) Install(req InstallRequest) (PluginStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.Mode == ModeDocker {
		return c.installDockerMode(req.ID)
	}
	return c.installLocal(req)
}

func (c *Center) installLocal(req InstallRequest) (PluginStatus, error) {
	// 切到本地模式：一体容器在跑会占着 8192/8193/8194 端口，本地引擎 bind 不上。
	// 先停容器释放端口（daemon 不可用时 docker stop 失败，忽略继续走本地启动）。
	if c.mode(req.ID) == ModeDocker || c.containerState(combinedCaptchaContainer).Exists {
		_ = procutil.Command(dockerExecutable(), "stop", combinedCaptchaContainer).Run()
	}
	c.setMode(req.ID, ModeLocal)
	// stop previous local process if any
	if old := c.takeProc(req.ID); old != nil && old.Process != nil {
		_ = old.Process.Kill()
	}

	logDir := c.runLogDir()
	_ = os.MkdirAll(logDir, 0o700)
	logPath := filepath.Join(logDir, string(req.ID)+".log")

	var cmd *exec.Cmd
	var startErr error

	switch req.ID {
	case PluginEzSolver:
		dir := filepath.Join(c.root, "plugins", "ezsolver")
		py, err := c.ensureEzSolverPython(dir, c.ezSolverVenvDir(), logPath)
		if err != nil {
			st := c.Status(req.ID)
			st.Message = err.Error()
			return st, err
		}
		cmd = procutil.Command(py, "-B", "service.py")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PORT=8192", "HOST=127.0.0.1", "EZSOLVER_HOST=127.0.0.1", "EZSOLVER_PORT=8192")

	case PluginVeloraTurn:
		cmd, startErr = c.cmdVeloraTurnLocal()
		if startErr != nil {
			st := c.Status(req.ID)
			st.Message = startErr.Error()
			return st, startErr
		}

	case PluginAuralith:
		cmd, startErr = c.cmdAuralithLocal()
		if startErr != nil {
			st := c.Status(req.ID)
			st.Message = startErr.Error()
			return st, startErr
		}

	default:
		return c.Status(req.ID), fmt.Errorf("unknown plugin %s", req.ID)
	}

	// 打码浏览器不上桌面：三个打码器都必须 headful（headless 的 UA 带
	// HeadlessChrome，Cloudflare 明确拦截），所以：
	//   Windows → 离屏偏移旗标；Linux → 本来就在 Xvfb 里；
	//   macOS   → 窗口保姆把窗口缩小推到角落并还原焦点
	//             （隐藏/最小化会让页面 visibilityState=hidden 被节流到 1~7%）
	cmd.Env = c.applyCaptchaOffscreenEnv(cmd.Env)
	// 打码并发上限：三个引擎统一在这里注入。旧实现只在 cmdAuralithLocal 里读
	// maxWorkers，EzSolver / VeloraTurn 的注入值存了却从没用过——实测启动后
	// EzSolver=12、VeloraTurn=18 个浏览器，与设置和注册并发都无关。
	cmd.Env = c.applyWorkerEnv(req.ID, cmd.Env)
	if c.nanny != nil {
		c.nanny.Start()
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		// keep file open for process lifetime
	}
	// 启动前按端口清理残留：app 重启后上一代的本地打码子进程不会跟着死
	// （孤儿），它们占着 8192/8193/8194 会让新实例绑定端口失败 ——
	// AutoStart / 手动安装都会走到这里，统一兜底（Docker 基础设施白名单内不杀）。
	if n := c.killPortOwner(captchaPort(req.ID)); n > 0 {
		log.Printf("[plugins] InstallLocal(%s): 已清理 %d 个占用 %d 端口的残留进程", req.ID, n, captchaPort(req.ID))
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		st := c.Status(req.ID)
		st.Message = "启动失败: " + err.Error() + " · log=" + logPath
		return st, err
	}
	c.setProc(req.ID, cmd)

	// wait for health up to ~8s
	deadline := time.Now().Add(8 * time.Second)
	var last PluginStatus
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		last = c.Status(req.ID)
		if last.Healthy {
			last.Message = "local started · " + last.Message + " · log=" + logPath
			return last, nil
		}
		// process exited early?
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			break
		}
	}
	last = c.Status(req.ID)
	tail := readLogTail(logPath, 1200)
	if tail != "" {
		last.Message = last.Message + " · logtail: " + tail
	} else {
		last.Message = last.Message + " · log=" + logPath
	}
	if !last.Healthy {
		return last, fmt.Errorf("插件已启动但健康检查未通过: %s", last.Message)
	}
	return last, nil
}

func (c *Center) ensureEzSolverPython(dir, venvDir, logPath string) (string, error) {
	venvPy := filepath.Join(venvDir, "bin", "python")
	if runtime.GOOS == "windows" {
		venvPy = filepath.Join(venvDir, "Scripts", "python.exe")
	}
	// reuse venv if nodriver import works
	if fileExists(venvPy) {
		check := procutil.Command(venvPy, "-c", "import nodriver")
		if check.Run() == nil {
			return venvPy, nil
		}
	}
	// nodriver 与 python 3.14 有编码兼容问题（SyntaxError: Non-UTF-8 code）。
	// 优先 3.13/3.12/3.11，再退回 python3。Windows 上优先官方 py launcher
	//（python3 不存在 → 9009 "command not found"；py 自动选最新 3.x）。
	// 仍未找到时，PATH 之外还要探测常见安装位置（注册表 + 默认安装目录），
	// 因为很多人装 Python 时没勾「Add to PATH」。
	py := "python3"
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("py"); err == nil {
			py = "py"
		} else {
			if p := findWindowsPython(); p != "" {
				py = p
			}
		}
	}
	for _, cand := range []string{"python3.13", "python3.12", "python3.11", "python3"} {
		if p, err := exec.LookPath(cand); err == nil {
			// 跳过 WindowsApps 的 App Execution Alias stub（0 字节重解析点）：
			// LookPath 会认为它「存在」，但直接执行会静默失败
			// （退出码 9009、零输出 —— 实测 venv 创建就死在这）。真 Python
			// 安装体积都在 MB 级，size==0 即可安全排除。
			if st, serr := os.Stat(p); serr == nil && st.Size() == 0 {
				continue
			}
			py = cand
			break
		}
	}
	if _, err := exec.LookPath(py); err != nil {
		py = "python"
		if _, err2 := exec.LookPath(py); err2 != nil {
			// 连 Store 占位符都没有 → 自动下载安装真实 Python
			if runtime.GOOS == "windows" {
				_ = appendFile(logPath, "未找到 Python，自动下载安装 python.org Python 3.12…\n")
				real, perr := ensureWindowsPython(logPath)
				if perr != nil || real == "" {
					return "", fmt.Errorf("未找到 python3/python 且自动安装失败：%v（可手动安装 https://www.python.org/downloads/ 后重试）", perr)
				}
				py = real
			} else {
				return "", fmt.Errorf("未找到 python3/python，无法启动 EzSolver")
			}
		}
	}
	// Windows 兜底：上面选出的 py 若仍是 Store stub（比如机器上只有
	// WindowsApps 的占位符、没装任何真实 Python），直接执行必然 9009。
	// 自动下载 python.org 安装器静默安装真实 Python（用户级，无需 UAC），
	// 装好用它继续建 venv —— 新电脑零手动干预。
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath(py); err == nil {
			if st, serr := os.Stat(p); serr == nil && st.Size() == 0 {
				_ = appendFile(logPath, "检测到 Python 仅为 Microsoft Store 占位符（App Execution Alias），自动下载安装 python.org Python 3.12…\n")
				real, perr := ensureWindowsPython(logPath)
				if perr != nil || real == "" {
					return "", fmt.Errorf("未检测到可用的 Python，自动安装失败：%v（可手动安装 https://www.python.org/downloads/ 后重试）", perr)
				}
				py = real
			}
		}
	}
	// create venv in the mutable state tree, never inside the signed payload.
	if err := os.MkdirAll(filepath.Dir(venvDir), 0o700); err != nil {
		return "", fmt.Errorf("创建 EzSolver 状态目录失败: %w", err)
	}
	_ = os.RemoveAll(venvDir)
	venv := procutil.Command(py, "-m", "venv", venvDir)
	venv.Dir = dir
	if out, err := venv.CombinedOutput(); err != nil {
		// fallback: system python + pip install --user
		_ = appendFile(logPath, "venv failed: "+string(out)+"\n")
		pip := procutil.Command(py, "-m", "pip", "install", "--user", "-q", "nodriver")
		if pout, perr := pip.CombinedOutput(); perr != nil {
			return "", fmt.Errorf("EzSolver 依赖安装失败(venv+pip): %v / %s", perr, trunc(string(pout), 400))
		}
		return py, nil
	}
	pipBin := filepath.Join(venvDir, "bin", "pip")
	if runtime.GOOS == "windows" {
		pipBin = filepath.Join(venvDir, "Scripts", "pip.exe")
	}
	pip := procutil.Command(pipBin, "install", "-q", "nodriver")
	pip.Dir = dir
	if out, err := pip.CombinedOutput(); err != nil {
		return "", fmt.Errorf("EzSolver pip install nodriver 失败: %v / %s", err, trunc(string(out), 400))
	}
	if !fileExists(venvPy) {
		return "", fmt.Errorf("venv python 不存在: %s", venvPy)
	}
	return venvPy, nil
}

func (c *Center) cmdVeloraTurnLocal() (*exec.Cmd, error) {
	dir := filepath.Join(c.root, "plugins", "veloraturn")
	// prefer OS binary if present
	cands := []string{
		filepath.Join(dir, pluginBinName("veloraturn")),
		filepath.Join(dir, "veloraturn"),
	}
	if runtime.GOOS == "windows" {
		cands = append(cands, filepath.Join(dir, "veloraturn.exe"))
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		cands = append([]string{filepath.Join(dir, "veloraturn-go-linux-amd64")}, cands...)
	}
	for _, b := range cands {
		if fileExists(b) {
			cmd := procutil.Command(b)
			cmd.Env = append(os.Environ(), "PORT=8193", "HOST=127.0.0.1")
			return cmd, nil
		}
	}
	src := filepath.Join(dir, "src")
	if fileExists(src) {
		if _, err := exec.LookPath("go"); err != nil {
			return nil, fmt.Errorf("VeloraTurn: 无预编译二进制且未安装 go，无法本地启动")
		}
		// build once into plugins/veloraturn/veloraturn-$GOOS-$GOARCH
		outBin := filepath.Join(dir, pluginBinName("veloraturn"))
		build := procutil.Command("go", "build", "-o", outBin, ".")
		build.Dir = src
		if bout, berr := build.CombinedOutput(); berr != nil {
			// fallback go run
			cmd := procutil.Command("go", "run", ".")
			cmd.Dir = src
			cmd.Env = append(os.Environ(), "PORT=8193")
			_ = bout
			return cmd, nil
		}
		cmd := procutil.Command(outBin)
		cmd.Env = append(os.Environ(), "PORT=8193", "HOST=127.0.0.1")
		return cmd, nil
	}
	return nil, fmt.Errorf("VeloraTurn: 未找到源码或二进制 (plugins/veloraturn)")
}

func (c *Center) cmdAuralithLocal() (*exec.Cmd, error) {
	dir := filepath.Join(c.root, "plugins", "auralith")
	cands := []string{
		filepath.Join(dir, pluginBinName("auralithd")),
		filepath.Join(dir, "auralithd"),
	}
	if runtime.GOOS == "windows" {
		cands = append(cands, filepath.Join(dir, "auralithd.exe"))
	}
	// legacy names
	if runtime.GOOS == "linux" {
		if runtime.GOARCH == "amd64" {
			cands = append(cands, filepath.Join(dir, "auralithd-linux-amd64"))
		}
		if runtime.GOARCH == "arm64" {
			cands = append(cands, filepath.Join(dir, "auralithd-linux-arm64"))
		}
	}
	// also search PATH
	cands = append(cands, "auralithd")

	var bin string
	for _, cand := range cands {
		if strings.Contains(cand, string(os.PathSeparator)) {
			if fileExists(cand) {
				bin = cand
				break
			}
		} else if lookPath(cand) {
			bin = cand
			break
		}
	}

	// try compile from monorepo source if present
	if bin == "" {
		// umbraforge root may be .../umbraforge; monorepo backend at ../backend/cmd/auralith
		srcCandidates := []string{
			filepath.Clean(filepath.Join(c.root, "..", "backend", "cmd", "auralith")),
			filepath.Clean(filepath.Join(c.root, "backend", "cmd", "auralith")),
		}
		if _, err := exec.LookPath("go"); err == nil {
			for _, src := range srcCandidates {
				if fileExists(src) {
					outBin := filepath.Join(dir, pluginBinName("auralithd"))
					build := procutil.Command("go", "build", "-o", outBin, ".")
					build.Dir = src
					if bout, berr := build.CombinedOutput(); berr != nil {
						return nil, fmt.Errorf("Auralith 本地编译失败: %v / %s", berr, trunc(string(bout), 400))
					}
					bin = outBin
					break
				}
			}
		}
	}

	if bin == "" {
		return nil, fmt.Errorf("Auralith: 未找到本机二进制 auralithd-%s-%s。请将可执行文件放入 plugins/auralith/ 或安装 go 以便从源码编译", runtime.GOOS, runtime.GOARCH)
	}

	// refuse clearly if linux binary on darwin
	if runtime.GOOS == "darwin" && strings.Contains(bin, "linux") {
		return nil, fmt.Errorf("Auralith: 当前是 Linux 二进制 (%s)，无法在 macOS 运行。请使用 auralithd-darwin-%s", filepath.Base(bin), runtime.GOARCH)
	}

	cmd := procutil.Command(bin)
	cmd.Env = append(os.Environ(),
		"PORT=8194",
		"AURALITH_PORT=8194",
		"AURALITH_BIND_HOST=127.0.0.1",
		// 注意：不要强制 AURALITH_POOL_MODE=context —— context 模式在 macOS
		// 上会导致 auralithd 进程存活但 8194 端口无监听（health 检查失败）。
		// 让 auralithd 使用自身默认（browser 模式）最稳定。
	)
	// 打码并发由 installLocal 统一经 applyWorkerEnv 注入（三个引擎一致）。
	return cmd, nil
}

func readLogTail(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	s := string(b)
	if len(s) > max {
		s = s[len(s)-max:]
	}
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " | ")
	return s
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// createContainerWithPayload 建容器 → docker cp 送文件 → start。
// 用 cp 而不是 -v：绑定挂载的源路径由 VM 内的守护进程解析，宿主目录没共享进 VM
// 时 Docker 会静默创建空目录顶上，容器里就是「文件变目录」→ exit 126。
// logf 可为 nil（同步安装路径不需要进度日志）。
// image 由调用方解析好传入（而不是内部再查一次 baseImage），避免部署过程中
// 守护进程抖动导致两次解析结果不一致（日志与实际用的镜像对不上）。
func (c *Center) createContainerWithPayload(id PluginID, name string, port int, image string, logf func(string)) error {
	log := func(s string) {
		if logf != nil {
			logf(s)
		}
	}
	payloads, err := c.dockerPayloads(id)
	if err != nil {
		return err
	}
	defer cleanupDockerPayloads(payloads)
	_ = procutil.Command(dockerExecutable(), "rm", "-f", name).Run()

	args := dockerCreateArgs(c.dockerRunArgsWithImage(id, name, port, image))
	log("$ docker " + strings.Join(args, " "))
	if out, err := procutil.Command(dockerExecutable(), args...).CombinedOutput(); err != nil {
		log(strings.TrimSpace(string(out)))
		return fmt.Errorf("创建容器失败: %v / %s", err, trunc(string(out), 300))
	}
	for _, p := range payloads {
		log(fmt.Sprintf("$ docker cp %s %s:%s", filepath.Base(p.Src), name, p.Dest))
		out, err := procutil.Command(dockerExecutable(), "cp", p.Src, name+":"+p.Dest).CombinedOutput()
		if err != nil {
			log(strings.TrimSpace(string(out)))
			_ = procutil.Command(dockerExecutable(), "rm", "-f", name).Run()
			return fmt.Errorf("送入 %s 失败: %v / %s", p.Dest, err, trunc(string(out), 300))
		}
	}
	if out, err := procutil.Command(dockerExecutable(), "start", name).CombinedOutput(); err != nil {
		log(strings.TrimSpace(string(out)))
		return fmt.Errorf("启动容器失败: %v / %s", err, trunc(string(out), 300))
	}
	return nil
}

// containerDied 若容器已退出则返回 (true, 退出码, 日志尾部)。
// 架构不符 / 文件不可执行会在 1~2 秒内 exit 126|127，没必要等满 10 分钟健康轮询。
func (c *Center) containerDied(name string) (bool, int, string) {
	out, err := procutil.Command(dockerExecutable(), "inspect",
		"--format", "{{.State.Running}} {{.State.ExitCode}}", name).Output()
	if err != nil {
		return false, 0, ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 || fields[0] != "false" {
		return false, 0, ""
	}
	code := 0
	_, _ = fmt.Sscanf(fields[1], "%d", &code)
	return true, code, c.containerLogTail(name)
}

func (c *Center) installDocker(req InstallRequest) (PluginStatus, error) {
	c.setMode(req.ID, ModeDocker)
	name := c.containerName(req.ID)
	port := captchaPort(req.ID)
	if req.Port > 0 {
		port = req.Port
	}
	if err := c.createContainerWithPayload(req.ID, name, port, c.baseImage(req.ID), nil); err != nil {
		st := c.Status(req.ID)
		st.Mode = ModeDocker
		st.Message = err.Error()
		return st, err
	}
	c.setDockerName(req.ID, name)
	time.Sleep(1200 * time.Millisecond)
	return c.Status(req.ID), nil
}

// dockerRunArgs 组装打码器容器的 docker run 参数。
// 拆出来是为了能在无 Docker 守护进程的环境下单测（浏览器/虚拟屏是否齐备）。
// 挂载源统一用 dockerMountSource() 复制到数据目录（/Users 下默认共享），
// 避免 /Applications 或 /Volumes 路径触发 Docker 的 mounts denied。
//
// 部署路径必须用 dockerRunArgsWithImage 把镜像定死，不能调用这个便捷版：
// baseImage() 判断「本地是否已有固化镜像」依赖一次 docker inspect，
// 部署过程里日志用它算一次、真正建容器时再算一次——中间守护进程哪怕
// 抖动一下，两次结果就可能不一致，日志说「复用已固化镜像」而实际用了
// debian:bookworm-slim，白跑一次 3~8 分钟的容器内 apt。
func (c *Center) dockerRunArgs(id PluginID, name string, port int) []string {
	return c.dockerRunArgsWithImage(id, name, port, c.baseImage(id))
}

// dockerRunArgsWithImage 与 dockerRunArgs 相同，但镜像由调用方传入而不是
// 内部再解析一次——部署路径应该用这个版本，把「用哪个镜像」的判断只做一次。
func (c *Center) dockerRunArgsWithImage(id PluginID, name string, port int, image string) []string {
	var args []string
	base := image
	switch id {
	case PluginEzSolver:
		// service.py 本来就监听 0.0.0.0；宿主侧只发布到 127.0.0.1，
		// 免费打码器默认无鉴权，不该暴露到局域网。
		args = []string{"run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:8192", port),
			"--label", "umbraforge.bootstrap=" + dockerBootstrapVersion,
			// 重启策略：Docker 引擎重启（colima restart / 改 VM 规格 / 开机）后
			// 容器要自己回来。缺这一条时容器被 SIGKILL 后永久停在 Exited(137)，
			// 而应用仍是 docker 模式 → 每次解题都连不上 → 注册全军覆没。
			"--restart", "unless-stopped",
			"-w", "/app", "-e", "PORT=8192",
			"-e", "CHROME_PATH=" + dockerChromePath,
			"-e", "TS_PROFILE_DIR=/tmp/ezsolver-profiles",
			// 容器内本来就是 Xvfb 虚拟屏，不需要再做离屏偏移
			"-e", "EZSOLVER_OFFSCREEN=0"}
		args = append(args, c.dockerWorkerEnv(id)...)
		args = append(args, "--shm-size=1g", // Chrome 共享内存不足会直接崩
			base, "bash", "-lc",
			dockerBrowserBootstrap+"pip install -q nodriver fastapi uvicorn httpx 2>/dev/null; "+
				dockerXvfbRun+"python service.py")
	case PluginVeloraTurn:
		args = []string{"run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:8193", port),
			"--label", "umbraforge.bootstrap=" + dockerBootstrapVersion,
			// 重启策略：Docker 引擎重启（colima restart / 改 VM 规格 / 开机）后
			// 容器要自己回来。缺这一条时容器被 SIGKILL 后永久停在 Exited(137)，
			// 而应用仍是 docker 模式 → 每次解题都连不上 → 注册全军覆没。
			"--restart", "unless-stopped",
			"-e", "PORT=8193",
			// 容器内必须监听 0.0.0.0，否则端口映射到不了（veloraturn 默认
			// BIND_HOST=127.0.0.1，只在容器 loopback 上听，健康检查永远失败）。
			// 宿主侧只发布到 127.0.0.1，不暴露到局域网。
			"-e", "BIND_HOST=0.0.0.0",
			"-e", "CHROME_PATH=" + dockerChromePath,
			"-e", "TS_PROFILE_DIR=/tmp/veloraturn-profiles",
			"-e", "EZSOLVER_OFFSCREEN=0"}
		args = append(args, c.dockerWorkerEnv(id)...)
		args = append(args, "--shm-size=1g",
			base, "bash", "-lc",
			// 二进制由 docker cp 落进容器可写层，chmod 直接生效（旧实现用 :ro 挂载，
			// colima 的挂载后端不保留执行位，还得先 cp 到 /opt 绕一圈）。
			dockerBrowserBootstrap+"chmod +x /usr/local/bin/veloraturn && "+
				dockerXvfbRun+"/usr/local/bin/veloraturn")
	case PluginAuralith:
		args = []string{"run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:8194", port),
			"--label", "umbraforge.bootstrap=" + dockerBootstrapVersion,
			// 重启策略：Docker 引擎重启（colima restart / 改 VM 规格 / 开机）后
			// 容器要自己回来。缺这一条时容器被 SIGKILL 后永久停在 Exited(137)，
			// 而应用仍是 docker 模式 → 每次解题都连不上 → 注册全军覆没。
			"--restart", "unless-stopped",
			"-e", "PORT=8194",
			// 同 VeloraTurn：auralithd 默认 AURALITH_BIND_HOST=127.0.0.1，
			// 容器内只听 loopback 时端口映射穿不进去（实测 /proc/net/tcp 为
			// 0100007F:2002），健康检查会一直失败到 10 分钟超时。
			"-e", "AURALITH_BIND_HOST=0.0.0.0",
			"-e", "CHROME_PATH=" + dockerChromePath,
			"-e", "AURALITH_PROFILE_DIR=/tmp/auralith-profiles"}
		args = append(args, c.dockerWorkerEnv(id)...)
		args = append(args, "--shm-size=1g",
			base, "bash", "-lc",
			dockerBrowserBootstrap+"chmod +x /usr/local/bin/auralithd && "+
				dockerXvfbRun+"/usr/local/bin/auralithd")
	}
	return args
}

// publicBaseImage 是各插件的公共基础镜像（容器内需自行 apt 装 Chromium+Xvfb）。
func publicBaseImage(id PluginID) string {
	if id == PluginEzSolver {
		return "python:3.11-slim"
	}
	return "debian:bookworm-slim"
}

// baseImage 优先用首次部署后 commit 的本地镜像（Chromium 已就绪，秒级启动），
// 没有则回落公共基础镜像。
func (c *Center) baseImage(id PluginID) string {
	if !protectedRelease() {
		if img := capturedImage(id); c.imageExists(img) {
			return img
		}
	}
	return publicBaseImage(id)
}

// dockerWorkerEnv 把打码并发上限注入容器（-e MAX_WORKERS=N）。
// 不注入时三个引擎都会按 CPU/内存 auto-tune：实测 EzSolver 默认 12、
// Auralith/VeloraTurn 拉到 18 个浏览器，与用户选的注册并发完全脱钩。
func (c *Center) dockerWorkerEnv(id PluginID) []string {
	n := c.clampToCapacity(id, c.MaxWorkers(id))
	if n <= 0 {
		return nil
	}
	return []string{"-e", fmt.Sprintf("MAX_WORKERS=%d", n)}
}

func (c *Center) Stop(id PluginID) (PluginStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cmd := c.takeProc(id); cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// 兜底：孤儿/残留的 local 进程不在 c.procs 里（app 重启后上一代的
	// 打码子进程不会跟着死），按插件端口清理，否则端口一直被占、
	// 切换 Docker 模式时容器映射 bind 失败。Windows 版按进程名过滤
	// Docker 基础设施，不会误杀容器端口转发。
	if n := c.killPortOwner(captchaPort(id)); n > 0 {
		log.Printf("[plugins] Stop(%s): 已清理 %d 个占用 %d 端口的残留进程", id, n, captchaPort(id))
	}
	// 一体容器：三个引擎共用生命周期，停止 = 停整个容器（不删除，保留 Chromium 环境）
	if c.mode(id) == ModeDocker && c.containerState(combinedCaptchaContainer).Exists {
		_ = procutil.Command(dockerExecutable(), "stop", combinedCaptchaContainer).Run()
	} else {
		_ = procutil.Command(dockerExecutable(), "rm", "-f", c.containerName(id)).Run()
	}
	return c.Status(id), nil
}

func pluginBinName(prefix string) string {
	name := fmt.Sprintf("%s-%s-%s", prefix, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func dockerExecutable() string {
	// IMPORTANT: must not call dockerExecutable() inside itself (stack overflow).
	if p, err := exec.LookPath("docker"); err == nil && p != "" {
		return p
	}
	// PATH 兜底：从 Finder / 资源管理器启动的 GUI 程序拿不到 shell 里的 PATH。
	var cands []string
	switch runtime.GOOS {
	case "windows":
		cands = []string{
			`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
			`C:\ProgramData\DockerDesktop\version-bin\docker.exe`,
			`C:\Program Files\Docker\Docker\resources\docker.exe`,
		}
		// 支持非 C 盘安装
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			cands = append(cands, filepath.Join(pf, "Docker", "Docker", "resources", "bin", "docker.exe"))
		}
	case "darwin":
		cands = []string{
			"/opt/homebrew/bin/docker",
			"/usr/local/bin/docker",
			"/Applications/Docker.app/Contents/Resources/bin/docker",
			"/usr/bin/docker",
		}
	default: // linux 及其它
		cands = []string{
			"/usr/bin/docker",
			"/usr/local/bin/docker",
			"/snap/bin/docker",
			"/opt/homebrew/bin/docker",
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	if runtime.GOOS == "windows" {
		return "docker.exe"
	}
	return "docker"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && (st.Mode().IsRegular() || st.IsDir())
}

func lookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
