package grokregister

// BatchConfig mirrors Kiro batch tuning for Grok register.
type BatchConfig struct {
	MaxProxyFails    int `json:"max_proxy_fails"`
	MinProxyInterval int `json:"min_proxy_interval"`
	MaxPerIP         int `json:"max_per_ip"`
	// ProxyCooldownMinutes 疑似被上游限流的代理挂起多久（分钟），到期自动放回工作集。
	//
	// 触发条件是「连续失败达 max_proxy_fails + 连通性验证通过 + 重连后出口 IP 没变」：
	// 代理自身是好的，换不掉出口，继续用只会一直撞同一个限流。此前这种代理会被
	// 无限重试（计数清零 → 重连同 IP → 再失败），永远不会让位给别的代理。
	//
	// 0 = 用默认值（不是「关闭」）。这一点刻意与本结构体多数字段一致：
	// 老配置文件没有这个字段，反序列化就是 0，若把 0 当成「关闭」，
	// 升级后冷却会在用户毫不知情的情况下静默失效。要关闭就设成负数。
	ProxyCooldownMinutes int `json:"proxy_cooldown_minutes"`
	// ProxyWorkingHeadroom 工作集在并发数之上多留几个代理槽。
	// 0 = 严格按并发数取（并发 1 就只用 1 个代理）。
	// 被淘汰的槽会从未探测的储备里即时补位，所以 0 也不会因为一个代理挂了就断批。
	ProxyWorkingHeadroom int `json:"proxy_working_headroom"`
	// ProxyPrecheckAll 为 true 时恢复旧行为：启动即预检整个代理池。
	// 默认 false —— 预检每个代理都要经它发一次请求，住宅代理按请求/流量计费，
	// 并发 1 却探完 37 个代理纯属浪费额度。
	ProxyPrecheckAll bool   `json:"proxy_precheck_all"`
	RateCapacity     int    `json:"rate_capacity"`
	RateBase         int    `json:"rate_base"`
	RateMin          int    `json:"rate_min"`
	StaggerSec       int    `json:"stagger_sec"`
	WarpRotateMode   string `json:"warp_rotate_mode"`
	WarpRotateEvery  int    `json:"warp_rotate_every"`
	OTPPollMs        int    `json:"otp_poll_ms"`
	HumanDelay       bool   `json:"human_delay"`
}

func DefaultBatchConfig() *BatchConfig {
	return &BatchConfig{
		// max_per_ip: per-proxy-URL success cap; too low forces reconnect thrash.
		// UI / default_batch_config can override; 100 fits residential sticky pools.
		MaxProxyFails: 3, MinProxyInterval: 0, MaxPerIP: 100,
		// 10 分钟：多数上游的短时限流窗口在几分钟量级，取 10 分钟留余量；
		// 池子小的时候也不至于长时间少一个可用出口。
		ProxyCooldownMinutes: 10,
		ProxyWorkingHeadroom: 0, ProxyPrecheckAll: false,
		RateCapacity: 50, RateBase: 50, RateMin: 10,
		StaggerSec: 0, WarpRotateMode: "none", WarpRotateEvery: 0,
		OTPPollMs: 500, HumanDelay: false,
	}
}
