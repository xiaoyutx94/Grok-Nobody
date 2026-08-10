package xai

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/mod/semver"
)

// CLI 版本动态跟随。
//
// 官方 Grok CLI 通过 https://x.ai/cli/stable 发布稳定版本号（2026-08 实测为 1.0.0）。
// 网关 cli-chat-proxy 对 x-grok-client-version 做兼容性识别，长期 pin 旧版本号
// （如 0.2.114）会构成“非官方客户端”的强信号。这里在 CLIClientVersion 基线之上
// 定期拉取官方 stable，校验 semver 不低于基线后缓存生效；拉取失败静默回落基线，
// 绝不因版本源故障影响请求路径。
const (
	cliVersionTTL     = 30 * time.Minute
	cliVersionFetchTO = 10 * time.Second
)

var (
	cliVersionMu      sync.RWMutex
	cliEffectiveVer   = CLIClientVersion
	cliVersionFetched time.Time
	cliRefreshing     atomic.Bool
)

func init() {
	// 启动时后台拉取一次，不阻塞服务启动。
	go refreshEffectiveCLIVersion()
}

// EffectiveCLIClientVersion 返回当前生效的 Grok CLI 客户端版本号。
// TTL 内直接返回缓存；过期时触发一次异步刷新（刷新失败保留旧值）。
// 任何时刻返回值都满足 semver 且不低于 CLIClientVersion 基线。
func EffectiveCLIClientVersion() string {
	cliVersionMu.RLock()
	ver := cliEffectiveVer
	last := cliVersionFetched
	cliVersionMu.RUnlock()
	if time.Since(last) >= cliVersionTTL && cliRefreshing.CompareAndSwap(false, true) {
		go func() {
			defer cliRefreshing.Store(false)
			refreshEffectiveCLIVersion()
		}()
	}
	return ver
}

// CLIUserAgentString 生成与当前生效版本一致的 billing/CLI 身份 UA。
func CLIUserAgentString() string {
	return "grok-pager/" + EffectiveCLIClientVersion() + " grok-shell/" + EffectiveCLIClientVersion() + " (macos; aarch64)"
}

// refreshEffectiveCLIVersion 拉取官方 stable 版本号并校验。任何失败都保持原值。
func refreshEffectiveCLIVersion() {
	latest, err := fetchStableCLIVersion()
	if err != nil {
		slog.Debug("xai_cli_version_fetch_failed", "error", err.Error(), "fallback", CLIStableBaseline())
		return
	}
	canonical := "v" + latest
	if !isStrictSemver(latest) {
		slog.Debug("xai_cli_version_invalid", "version", latest)
		return
	}
	minimum := "v" + CLIStableBaseline()
	if semver.Compare(canonical, minimum) < 0 {
		slog.Debug("xai_cli_version_below_baseline", "version", latest, "baseline", CLIStableBaseline())
		return
	}
	cliVersionMu.Lock()
	cliEffectiveVer = latest
	cliVersionFetched = time.Now()
	cliVersionMu.Unlock()
	slog.Info("xai_cli_version_updated", "version", latest)
}

func fetchStableCLIVersion() (string, error) {
	client := &http.Client{Timeout: cliVersionFetchTO}
	req, err := http.NewRequest(http.MethodGet, CLIStableVersionURL(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "sub2api-grok/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", CLIStableVersionURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: http %d", CLIStableVersionURL(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", CLIStableVersionURL(), err)
	}
	ver := strings.TrimSpace(string(body))
	if ver == "" {
		return "", fmt.Errorf("fetch %s: empty body", CLIStableVersionURL())
	}
	return ver, nil
}
