package engine

import (
	"encoding/json"
	"testing"
)

// 注册页「使用代理池」取消勾选后必须真直连：
// 前端只发空 proxy_urls 时会被服务端回填代理池，所以要靠 use_proxy_pool=false 表达意图。
func TestStartInputPoolOptOut(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"未传字段时保持回填（兼容旧客户端）", `{"count":1}`, false},
		{"显式 true 时保持回填", `{"count":1,"use_proxy_pool":true}`, false},
		{"显式 false 时必须直连", `{"count":1,"use_proxy_pool":false}`, true},
		{"显式 false 且带空池", `{"count":1,"use_proxy_pool":false,"proxy_urls":[]}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var in StartInput
			if err := json.Unmarshal([]byte(c.body), &in); err != nil {
				t.Fatalf("unmarshal %s: %v", c.body, err)
			}
			if got := in.PoolOptOut(); got != c.want {
				t.Fatalf("PoolOptOut() = %v, want %v (body=%s)", got, c.want, c.body)
			}
		})
	}
}

// 前端把整个 form 展开进 body，字段名必须和 StartInput 对得上，
// 否则 use_proxy_pool 会被静默丢弃、直连意图再次失效。
func TestStartInputDecodesFrontendPayload(t *testing.T) {
	// 与 RegisterView.vue 的 form 一致的最小载荷
	body := `{
		"count": 3, "concurrency": 2, "delay": 1, "step_delay": 0,
		"email_mode": "edu", "captcha_provider": "ezsolver",
		"browser": "chrome", "os_type": "macos", "skip_verify": true,
		"proxy": "", "outlook_data": "", "use_proxy_pool": false,
		"cf_worker_ids": ["w1","w2"]
	}`
	var in StartInput
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Count != 3 || in.Concurrency != 2 {
		t.Fatalf("count/concurrency 解析错误: %d/%d", in.Count, in.Concurrency)
	}
	if in.EmailMode != "edu" || len(in.CFWorkerIDs) != 2 {
		t.Fatalf("edu 模式载荷解析错误: %q %v", in.EmailMode, in.CFWorkerIDs)
	}
	if !in.PoolOptOut() {
		t.Fatal("use_proxy_pool=false 未被识别为直连")
	}
}
