package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/umbraforge/desktop/internal/engine"
	"github.com/umbraforge/desktop/internal/store"
)

// GatewayService 本地 Grok 反代网关：生命周期 + 转发计数。
// 监听 127.0.0.1:<port>（默认 18789，独立于主 UI 17890）。
type GatewayService struct {
	store  *GatewayStore
	accts  *engine.AccountService
	picker *AccountPicker

	mu       sync.Mutex
	srv      *http.Server
	ln       net.Listener
	addr     string
	started  time.Time
	config   GatewayConfig

	requests atomic.Int64 // 累计请求数
	failures atomic.Int64 // 累计失败数
}

func NewGatewayService(st *store.JSONStore, accts *engine.AccountService) *GatewayService {
	return &GatewayService{
		store:  NewGatewayStore(st),
		accts:  accts,
		picker: NewAccountPicker(accts),
	}
}

// Store 暴露存储（admin 管理路由用）。
func (g *GatewayService) Store() *GatewayStore { return g.store }

// Picker 暴露账号选择器（admin 状态展示用）。
func (g *GatewayService) Picker() *AccountPicker { return g.picker }

// Start 初始化默认数据并按配置启动网关。配置 disabled 时仅初始化不监听。
// 返回 (监听地址, 是否在监听, error)。
func (g *GatewayService) Start(ctx context.Context) (string, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.store.EnsureDefaults(ctx); err != nil {
		return "", false, fmt.Errorf("网关数据初始化失败: %w", err)
	}
	cfg, err := g.store.GetConfig(ctx)
	if err != nil {
		return "", false, err
	}
	g.config = cfg
	if !cfg.Enabled {
		return "", false, nil
	}
	return g.startLocked(cfg)
}

func (g *GatewayService) startLocked(cfg GatewayConfig) (string, bool, error) {
	if g.srv != nil {
		return g.addr, true, nil // 已在运行
	}
	host := strings.TrimSpace(cfg.ListenHost)
	if host == "" {
		host = DefaultListenHost
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, cfg.Port))
	if err != nil {
		return "", false, fmt.Errorf("网关端口 %d 监听失败（可能被占用）: %w", cfg.Port, err)
	}
	handler := g.routes()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[gateway] serve: %v", err)
		}
	}()
	g.srv = srv
	g.ln = ln
	g.addr = ln.Addr().String()
	g.started = time.Now()
	log.Printf("[gateway] Grok 反代网关已启动: http://%s（OpenAI /v1/responses · /v1/chat/completions · Claude /v1/messages）", g.addr)
	return g.addr, true, nil
}

// Stop 停止网关。
func (g *GatewayService) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = g.srv.Shutdown(ctx)
	g.srv = nil
	g.ln = nil
	g.addr = ""
	log.Printf("[gateway] Grok 反代网关已停止")
}

// ApplyConfig 保存配置并热应用（启用→启动；停用→停止；端口变化→重启）。
func (g *GatewayService) ApplyConfig(ctx context.Context, cfg GatewayConfig) error {
	g.mu.Lock()
	if err := g.store.SetConfig(ctx, cfg); err != nil {
		g.mu.Unlock()
		return err
	}
	g.config = cfg
	wasRunning := g.srv != nil
	if !cfg.Enabled {
		g.mu.Unlock()
		g.Stop()
		return nil
	}
	if wasRunning && cfg.Port == g.portOf() {
		g.mu.Unlock()
		return nil // 配置未变，无需重启
	}
	g.mu.Unlock()
	g.Stop()
	_, _, err := g.Start(ctx)
	return err
}

func (g *GatewayService) portOf() int {
	if g.ln != nil {
		if a, ok := g.ln.Addr().(*net.TCPAddr); ok {
			return a.Port
		}
	}
	return g.config.Port
}

// LocalAddresses 返回本机可访问的 IPv4 地址列表（供网关页展示内外网地址）。
// 排除 loopback 与虚拟网卡；与网关同端口拼接。
func (g *GatewayService) LocalAddresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	port := g.portOf()
	var out []string
	seen := map[string]bool{}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		ip := ipnet.IP.To4().String()
		if seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, fmt.Sprintf("%s:%d", ip, port))
	}
	return out
}

// Status 网关运行状态。
type Status struct {
	Running    bool   `json:"running"`
	Addr       string `json:"addr,omitempty"`
	Port       int    `json:"port"`
	Enabled    bool   `json:"enabled"`
	Requests   int64  `json:"requests"`
	Failures   int64  `json:"failures"`
	StartedAt  string `json:"started_at,omitempty"`
	UptimeSec  int64  `json:"uptime_sec,omitempty"`
}

// Status 返回当前状态。
func (g *GatewayService) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := Status{
		Running:  g.srv != nil,
		Addr:     g.addr,
		Port:     g.config.Port,
		Enabled:  g.config.Enabled,
		Requests: g.requests.Load(),
		Failures: g.failures.Load(),
	}
	if g.srv != nil {
		st.StartedAt = g.started.UTC().Format(time.RFC3339)
		st.UptimeSec = int64(time.Since(g.started).Seconds())
	}
	return st
}

// CountRequest 转发计数（handler 调用）。
func (g *GatewayService) CountRequest(ok bool) {
	if ok {
		g.requests.Add(1)
	} else {
		g.failures.Add(1)
	}
}
