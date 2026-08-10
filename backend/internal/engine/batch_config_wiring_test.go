package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/umbraforge/desktop/internal/pkg/grokregister"
	"github.com/umbraforge/desktop/internal/store"
)

func newTestEngine(t *testing.T) *RegisterEngine {
	t.Helper()
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return NewRegisterEngine(st, NewAccountService(st))
}

// TestBatchConfigRoundTrips 批次调优必须能存下来。
// 此前 BatchConfig 根本不在 Config 里，batch.go 只能回落 DefaultBatchConfig()，
// 于是「失败几次淘汰 / 每槽成功上限 / 粘性间隔」这些全是硬编码、UI 改不动。
func TestBatchConfigRoundTrips(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	cfg, err := e.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.BatchConfig == nil {
		t.Fatal("默认配置应带 BatchConfig，否则前端拿不到初值")
	}

	cfg.BatchConfig.MaxProxyFails = 7
	cfg.BatchConfig.MaxPerIP = 3
	cfg.BatchConfig.MinProxyInterval = 1500
	cfg.BatchConfig.ProxyWorkingHeadroom = 2
	cfg.BatchConfig.StaggerSec = 4
	cfg.BatchConfig.OTPPollMs = 800
	cfg.BatchConfig.HumanDelay = true
	cfg.BatchConfig.ProxyPrecheckAll = true
	cfg.BatchConfig.WarpRotateMode = "per_n"
	cfg.BatchConfig.WarpRotateEvery = 5
	cfg.BatchConfig.RateCapacity = 30
	cfg.BatchConfig.RateBase = 25
	cfg.BatchConfig.RateMin = 5

	if _, err := e.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := e.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig2: %v", err)
	}
	if got.BatchConfig == nil {
		t.Fatal("保存后 BatchConfig 丢了")
	}
	bc := got.BatchConfig
	checks := map[string][2]int{
		"MaxProxyFails":        {bc.MaxProxyFails, 7},
		"MaxPerIP":             {bc.MaxPerIP, 3},
		"MinProxyInterval":     {bc.MinProxyInterval, 1500},
		"ProxyWorkingHeadroom": {bc.ProxyWorkingHeadroom, 2},
		"StaggerSec":           {bc.StaggerSec, 4},
		"OTPPollMs":            {bc.OTPPollMs, 800},
		"WarpRotateEvery":      {bc.WarpRotateEvery, 5},
		"RateCapacity":         {bc.RateCapacity, 30},
		"RateBase":             {bc.RateBase, 25},
		"RateMin":              {bc.RateMin, 5},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d, want %d", name, pair[0], pair[1])
		}
	}
	if !bc.HumanDelay {
		t.Error("HumanDelay 未保存")
	}
	if !bc.ProxyPrecheckAll {
		t.Error("ProxyPrecheckAll 未保存")
	}
	if bc.WarpRotateMode != "per_n" {
		t.Errorf("WarpRotateMode = %q", bc.WarpRotateMode)
	}
}

// TestSaveConfigKeepsBatchConfigOnPartialUpdate 前端只改打码 Key 时
// 不传 batch_config，不能把已保存的调优清成 nil/默认。
func TestSaveConfigKeepsBatchConfigOnPartialUpdate(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	cfg, _ := e.GetConfig(ctx)
	cfg.BatchConfig.MaxPerIP = 9
	if _, err := e.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// 模拟局部保存：只带打码字段，batch_config 为 nil
	partial := Config{CaptchaProvider: "auralith"}
	if _, err := e.SaveConfig(ctx, partial); err != nil {
		t.Fatalf("partial SaveConfig: %v", err)
	}
	got, _ := e.GetConfig(ctx)
	if got.BatchConfig == nil {
		t.Fatal("局部保存把 BatchConfig 清成 nil 了")
	}
	if got.BatchConfig.MaxPerIP != 9 {
		t.Errorf("MaxPerIP = %d, want 9（局部保存不该覆盖）", got.BatchConfig.MaxPerIP)
	}
	if got.CaptchaProvider != "auralith" {
		t.Errorf("CaptchaProvider = %q", got.CaptchaProvider)
	}
}

// TestNormalizeBatchConfigZeroSemantics 区分「零值=未设置」与「零值合法」。
func TestNormalizeBatchConfigZeroSemantics(t *testing.T) {
	def := grokregister.DefaultBatchConfig()

	// 全零输入：允许为 0 的保持 0，其余回落默认
	out := normalizeBatchConfig(&grokregister.BatchConfig{})
	if out.MaxProxyFails != def.MaxProxyFails {
		t.Errorf("MaxProxyFails 应回落默认，实际 %d", out.MaxProxyFails)
	}
	if out.MaxPerIP != def.MaxPerIP {
		t.Errorf("MaxPerIP 应回落默认，实际 %d", out.MaxPerIP)
	}
	if out.RateCapacity != def.RateCapacity || out.RateBase != def.RateBase || out.RateMin != def.RateMin {
		t.Errorf("速率参数应回落默认: %+v", out)
	}
	if out.OTPPollMs != def.OTPPollMs {
		t.Errorf("OTPPollMs 应回落默认，实际 %d", out.OTPPollMs)
	}
	if out.WarpRotateMode != def.WarpRotateMode {
		t.Errorf("WarpRotateMode 应回落默认，实际 %q", out.WarpRotateMode)
	}
	// 这几个 0 是合法值，不能被改写
	if out.MinProxyInterval != 0 {
		t.Errorf("MinProxyInterval 0 应保持，实际 %d", out.MinProxyInterval)
	}
	if out.StaggerSec != 0 {
		t.Errorf("StaggerSec 0 应保持，实际 %d", out.StaggerSec)
	}
	if out.ProxyWorkingHeadroom != 0 {
		t.Errorf("ProxyWorkingHeadroom 0 应保持（严格按并发数），实际 %d", out.ProxyWorkingHeadroom)
	}
	if out.WarpRotateEvery != 0 {
		t.Errorf("WarpRotateEvery 0 应保持，实际 %d", out.WarpRotateEvery)
	}

	// nil 输入返回默认，不 panic
	if got := normalizeBatchConfig(nil); got == nil || got.MaxProxyFails != def.MaxProxyFails {
		t.Errorf("nil 输入应返回默认: %+v", got)
	}

	// 负数被夹到 0
	neg := normalizeBatchConfig(&grokregister.BatchConfig{
		MinProxyInterval: -5, StaggerSec: -1, ProxyWorkingHeadroom: -3, WarpRotateEvery: -2,
	})
	if neg.MinProxyInterval != 0 || neg.StaggerSec != 0 || neg.ProxyWorkingHeadroom != 0 || neg.WarpRotateEvery != 0 {
		t.Errorf("负数应夹到 0: %+v", neg)
	}

	// 不能修改入参（调用方可能复用）
	in := &grokregister.BatchConfig{MaxProxyFails: 0}
	_ = normalizeBatchConfig(in)
	if in.MaxProxyFails != 0 {
		t.Error("normalizeBatchConfig 不应修改入参")
	}
}

// TestConfigJSONExposesBatchConfig 前端靠 JSON 字段名读写，键名必须是 batch_config。
func TestConfigJSONExposesBatchConfig(t *testing.T) {
	cfg := DefaultConfig()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	raw, ok := m["batch_config"]
	if !ok {
		t.Fatal("Config JSON 里没有 batch_config，前端读不到")
	}
	inner, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("batch_config 类型不对: %T", raw)
	}
	for _, k := range []string{
		"max_proxy_fails", "min_proxy_interval", "max_per_ip",
		"proxy_working_headroom", "proxy_precheck_all",
		"rate_capacity", "rate_base", "rate_min", "stagger_sec",
		"warp_rotate_mode", "warp_rotate_every", "otp_poll_ms", "human_delay",
	} {
		if _, ok := inner[k]; !ok {
			t.Errorf("batch_config 缺字段 %q", k)
		}
	}
}

// TestOtherConfigFieldsRoundTrip 这批字段此前也没有 UI，一并固定住存取。
func TestOtherConfigFieldsRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	cfg, _ := e.GetConfig(ctx)

	cfg.AutoExport = true
	cfg.AutoImport = true
	cfg.ImportConvertInflight = 8
	cfg.ImportProxyMode = "direct"
	cfg.ProxyPickMode = "round_robin"
	cfg.AutoPauseOnFailures = true
	cfg.AutoPauseWaitMinutes = 12
	cfg.AutoPauseFailThreshold = 25
	cfg.DefaultDelay = 3
	cfg.DefaultStepDelay = 2

	if _, err := e.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, _ := e.GetConfig(ctx)

	if !got.AutoExport {
		t.Error("AutoExport 未保存")
	}
	if !got.AutoImport {
		t.Error("AutoImport 未保存")
	}
	if got.ImportConvertInflight != 8 {
		t.Errorf("ImportConvertInflight = %d", got.ImportConvertInflight)
	}
	if got.ImportProxyMode != "direct" {
		t.Errorf("ImportProxyMode = %q", got.ImportProxyMode)
	}
	if got.ProxyPickMode != "round_robin" {
		t.Errorf("ProxyPickMode = %q", got.ProxyPickMode)
	}
	if got.AutoPauseWaitMinutes != 12 || got.AutoPauseFailThreshold != 25 {
		t.Errorf("自动暂停参数 = %d/%d", got.AutoPauseWaitMinutes, got.AutoPauseFailThreshold)
	}
	if got.DefaultDelay != 3 || got.DefaultStepDelay != 2 {
		t.Errorf("延迟 = %d/%d", got.DefaultDelay, got.DefaultStepDelay)
	}
}
