package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/umbraforge/desktop/internal/pkg/grokregister"
)

// TestRunPassesBatchConfigToBatchOptions 源码级断言：run() 必须把配置传进
// BatchOptions。此前这里没有赋值，batch.go 只能回落 DefaultBatchConfig()，
// 于是整组调优参数静默失效——这种「不报错但功能不通」最容易复发。
func TestRunPassesBatchConfigToBatchOptions(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("register.go"))
	if err != nil {
		t.Fatalf("read register.go: %v", err)
	}
	body := string(src)
	// 取 BatchOptions 字面量块
	start := strings.Index(body, "opts := grokregister.BatchOptions{")
	if start < 0 {
		t.Fatal("找不到 BatchOptions 字面量")
	}
	block := body[start:]
	if end := strings.Index(block, "\n\t}"); end > 0 {
		block = block[:end]
	}
	if !regexp.MustCompile(`BatchConfig:\s*normalizeBatchConfig\(cfg\.BatchConfig\)`).MatchString(block) {
		t.Error("BatchOptions 未传入 normalizeBatchConfig(cfg.BatchConfig)，" +
			"批次调优会静默回落硬编码默认值")
	}
}

// TestDefaultBatchConfigMatchesFrontendDefaults 前端 reactive 初值必须与后端默认一致，
// 否则用户没点保存时看到的数字是假的。
func TestDefaultBatchConfigMatchesFrontendDefaults(t *testing.T) {
	vue, err := os.ReadFile(filepath.Join("..", "..", "..",
		"frontend", "src", "views", "SettingsView.vue"))
	if err != nil {
		t.Skipf("读不到 SettingsView.vue: %v", err)
	}
	def := grokregister.DefaultBatchConfig()
	text := string(vue)
	want := map[string]int{
		"max_proxy_fails":        def.MaxProxyFails,
		"max_per_ip":             def.MaxPerIP,
		"min_proxy_interval":     def.MinProxyInterval,
		"proxy_working_headroom": def.ProxyWorkingHeadroom,
		"rate_capacity":          def.RateCapacity,
		"rate_base":              def.RateBase,
		"rate_min":               def.RateMin,
		"stagger_sec":            def.StaggerSec,
		"otp_poll_ms":            def.OTPPollMs,
		"warp_rotate_every":      def.WarpRotateEvery,
	}
	for key, val := range want {
		re := regexp.MustCompile(key + `:\s*(-?\d+)`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("SettingsView 里找不到 %s 的初值", key)
			continue
		}
		if m[1] != itoa(val) {
			t.Errorf("%s 前端初值 %s，后端默认 %d —— 不一致会误导用户", key, m[1], val)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
