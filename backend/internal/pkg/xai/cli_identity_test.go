package xai

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestCLIClientVersionMatchesSub2API 钉住两边 CLI 版本不许漂移。
//
// CLI 网关（cli-chat-proxy.grok.com）按 x-grok-client-version 判客户端新旧：
// 缺头直接 426「Your Grok CLI version (none) is outdated」，版本过旧同样会被拒。
// umbraforge 曾停在 0.2.93 而 sub2api 已是 0.2.114 —— 这种落后是隐形定时炸弹，
// 上游抬高下限时 umbraforge 会突然全量 426。
func TestCLIClientVersionMatchesSub2API(t *testing.T) {
	// umbraforge/backend/internal/pkg/xai → 上溯到仓库根再进 backend
	sub2apiBilling := filepath.Join("..", "..", "..", "..", "..",
		"backend", "internal", "pkg", "xai", "billing.go")
	raw, err := os.ReadFile(sub2apiBilling)
	if err != nil {
		t.Skipf("找不到 sub2api 的 billing.go（%v），跳过跨仓比对", err)
	}
	re := regexp.MustCompile(`CLIClientVersion\s*=\s*"([^"]+)"`)
	m := re.FindSubmatch(raw)
	if len(m) < 2 {
		t.Skip("未能从 sub2api billing.go 解析出 CLIClientVersion")
	}
	want := string(m[1])
	if CLIClientVersion != want {
		t.Errorf("CLIClientVersion = %q，sub2api 是 %q —— 两边必须同步升级，"+
			"否则 CLI 网关可能因版本过旧回 426", CLIClientVersion, want)
	}
}

// TestCLIClientVersionAboveGatewayFloor 网关当前公布的下限是 0.1.202。
func TestCLIClientVersionAboveGatewayFloor(t *testing.T) {
	const floor = "0.1.202"
	if compareSemver(CLIClientVersion, floor) < 0 {
		t.Errorf("CLIClientVersion = %q 低于网关下限 %s，会被判 outdated",
			CLIClientVersion, floor)
	}
}

func TestCLIUserAgentEmbedsVersion(t *testing.T) {
	ua := CLIUserAgentString()
	if !strings.Contains(ua, CLIClientVersion) {
		t.Errorf("UA %q 未包含版本 %q", ua, CLIClientVersion)
	}
	// 官方 grok-build 格式：grok-shell/<version> (<os>; <arch>)。
	// 服务端按该格式解析客户端（xai-grok-sampler client.rs 注释：
	// "pieces are wire contract for server-side UA parsing"）。
	if !strings.HasPrefix(ua, "grok-shell/") {
		t.Errorf("UA 必须以 grok-shell/ 开头（官方单一产品格式）: %q", ua)
	}
	if !regexp.MustCompile(`^grok-shell/[0-9]+\.[0-9]+\.[0-9]+ \([a-z0-9]+; [a-z0-9_]+\)$`).MatchString(ua) {
		t.Errorf("UA 不符合官方格式 grok-shell/<v> (<os>; <arch>): %q", ua)
	}
	// 不能带 HeadlessChrome 之类特征（这是 Turnstile/网关的识别点）
	if strings.Contains(strings.ToLower(ua), "headless") {
		t.Errorf("UA 不应含 headless: %q", ua)
	}
	// 不得拼接多产品前缀（旧格式 grok-pager/… grok-shell/… 是错误格式）
	if strings.Contains(ua, "grok-pager") {
		t.Errorf("UA 不应含 grok-pager 前缀（官方单一产品格式）: %q", ua)
	}
}

func TestCLIIdentityHeaderNames(t *testing.T) {
	// 头名固定，写错等于没带（网关回 426）
	if CLITokenAuthHeader != "x-xai-token-auth" {
		t.Errorf("CLITokenAuthHeader = %q", CLITokenAuthHeader)
	}
	if CLITokenAuthValue != "xai-grok-cli" {
		t.Errorf("CLITokenAuthValue = %q", CLITokenAuthValue)
	}
	if CLIClientVersionHeader != "x-grok-client-version" {
		t.Errorf("CLIClientVersionHeader = %q", CLIClientVersionHeader)
	}
}

// compareSemver 比较 a、b（形如 x.y.z）；a<b 返回 -1，相等 0，a>b 1。
func compareSemver(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
