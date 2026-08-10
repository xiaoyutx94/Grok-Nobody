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
	"runtime"
	"syscall"
	"time"

	"github.com/umbraforge/desktop/internal/engine"
	"github.com/umbraforge/desktop/internal/plugins"
	"github.com/umbraforge/desktop/internal/server"
	"github.com/umbraforge/desktop/internal/store"
	"github.com/umbraforge/desktop/internal/web"
	webview "github.com/webview/webview_go"
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
	api := &server.API{
		Engine:   engine.NewRegisterEngine(st, accounts),
		Edu:      engine.NewEduService(st),
		Plugins:  pluginCenter,
		Accounts: accounts,
		Warp:     warp,
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
		runtime.LockOSThread()
		// Install Cocoa Edit menu so Cmd+C/V/X/A reach WKWebView inputs.
		installNativeEditMenu()
		w := webview.New(false)
		defer w.Destroy()
		w.SetTitle("Grok-Nobody")
		w.SetSize(1440, 920, webview.HintNone)
		applyWindowIcon(w)
		// Clipboard: native Cocoa Edit menu only (no JS paste — avoids double insert).
		w.Navigate(ui)
		w.Run()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
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
