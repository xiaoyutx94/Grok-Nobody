package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/umbraforge/desktop/internal/engine"
	enginegateway "github.com/umbraforge/desktop/internal/engine/gateway"
	"github.com/umbraforge/desktop/internal/plugins"
	"github.com/umbraforge/desktop/internal/server"
	"github.com/umbraforge/desktop/internal/store"
	"github.com/umbraforge/desktop/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "HTTP listen address")
	root := flag.String("root", "", "plugins root (development override)")
	dataDirFlag := flag.String("data-dir", "", "application data directory (test/portable override)")
	headless := flag.Bool("headless", false, "API only")
	fixedURL := flag.String("url", "", "override UI url")
	flag.Parse()

	dataDir := *dataDirFlag
	var err error
	if dataDir == "" {
		dataDir, err = store.DataDir()
		if err != nil {
			log.Fatal(err)
		}
	}
	st, err := store.NewJSONStore(dataDir)
	if err != nil {
		log.Fatal(err)
	}

	// 单实例锁：双开会互相覆盖 settings.json（账号/密钥丢失），
	// 且第二实例退出会触发 macOS 系统崩溃（CoreSpotlight atexit in fork child）。
	// 冲突时用 _exit 退出（绕过 atexit 清理链），不弹崩溃报告。
	if release, lerr := store.AcquireLock(dataDir); lerr != nil {
		log.Printf("⚠ 单实例冲突: %v（程序已在运行，本实例退出）", lerr)
		notifyAlreadyRunning()
		exitNoAtexit(1)
	} else {
		defer release()
	}
	projectRoot, err := resolveProjectRoot(*root, dataDir)
	if err != nil {
		log.Fatal(err)
	}

	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}

	accounts := engine.NewAccountService(st)
	warp := engine.NewWarpService(st, accounts)
	pluginCenter := plugins.NewCenterWithState(projectRoot, filepath.Join(dataDir, "plugin-state"))
	gw := enginegateway.NewGatewayService(st, accounts)
	api := &server.API{
		Engine:   engine.NewRegisterEngine(st, accounts),
		Edu:      engine.NewEduService(st),
		Plugins:  pluginCenter,
		Accounts: accounts,
		Warp:     warp,
		Gateway:  gw,
		Static:   sub,
	}
	r := api.Router()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	base := fmt.Sprintf("http://%s", ln.Addr().String())
	log.Printf("Grok-Nobody %s", base)
	log.Printf("data: %s", dataDir)
	log.Printf("plugins: %s", projectRoot)

	// 打码并发注入：插件中心作为下发目标，engine 侧按「设置上限 ∩ 注册并发」
	// 算出闸门后推过来（启动时推一次，之后每次保存设置 / 开始注册都会重推）。
	// 不注入的话三个引擎都按 CPU auto-tune：实测 EzSolver 12、Auralith 18 个浏览器。
	api.Engine.SetCaptchaWorkerSink(pluginWorkerSink{c: api.Plugins})
	if cfg, err := api.Engine.GetConfig(context.Background()); err == nil {
		api.Engine.PushCaptchaWorkers(cfg)
	}

	// 启动后自动以本地模式启动内置免费打码（EzSolver/VeloraTurn/Auralith）
	go func() {
		time.Sleep(4 * time.Second)
		api.Plugins.AutoStartLocalBundled()
	}()

	// 启动 Grok 反代网关（默认 127.0.0.1:18789，失败仅告警不退出）
	if addr, running, gerr := gw.Start(context.Background()); gerr != nil {
		log.Printf("[gateway] 启动失败: %v", gerr)
	} else if running {
		log.Printf("[gateway] API 网关已就绪: http://%s", addr)
	}

	srv := &http.Server{Handler: r}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("serve: %v", err)
		}
	}()

	ui := *fixedURL
	if ui == "" {
		ui = base + "/"
	}

	if *headless {
		waitSignal()
	} else {
		runUI(ui)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	gw.Stop()
}

func resolveRoot(root string) string {
	if root != "" {
		return root
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			dir,
			filepath.Clean(filepath.Join(dir, "..", "Resources")),
			filepath.Clean(filepath.Join(dir, "..", "Resources", "plugins", "..")),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd, filepath.Clean(filepath.Join(wd, "..")), filepath.Clean(filepath.Join(wd, "../..")))
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "plugins")); err == nil && st.IsDir() {
			return c
		}
	}
	// 找不到 plugins 目录时绝不静默用 wd：Windows 上用户常把 exe 单独拷走，
	// 静默 fallback 会把 data 目录建到桌面且三个打码引擎全部「找不到插件」。
	// 打一条明确的错误日志，前端也会看到「打码引擎缺失」状态。
	if wd, err := os.Getwd(); err == nil {
		log.Printf("[root] 警告: 未找到 plugins 目录（exe 旁的 plugins/ 或 Resources/plugins 均不存在），已回退到工作目录 %s。Windows 分发请保持 exe 与 plugins/、web/ 在同一目录。", wd)
		return wd
	}
	return "."
}

func waitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

// pluginWorkerSink 把 engine 算出的打码并发闸转给插件中心。
// 用适配器而不是让 engine 直接依赖 plugins 包（engine 只认字符串 ID）。
type pluginWorkerSink struct{ c *plugins.Center }

func (s pluginWorkerSink) SetMaxWorkers(id string, n int) {
	s.c.SetMaxWorkers(plugins.PluginID(id), n)
}

func (s pluginWorkerSink) EnsureCaptchaReady(id string) (string, error) {
	return s.c.EnsureCaptchaReady(id)
}
