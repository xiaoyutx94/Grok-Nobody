package gateway

// 网关数据模型：分组 / API Key / 服务（上游渠道）/ 网关配置。
// 概念对齐 sub2api：分组=groups（含分组级代理）、Key=api_keys、服务=channels（渠道）。

// GatewayGroup 账号分组：网关按 API Key → 分组 → 账号池路由。
// ProxyIDs 引用代理池条目 URL（engine.ProxyEntry.URL），空 = 直连。
type GatewayGroup struct {
	ID        string   `json:"id"`                  // uuid
	Name      string   `json:"name"`
	Desc      string   `json:"desc,omitempty"`
	ServiceID string   `json:"service_id,omitempty"` // 绑定服务；空 = 默认 grok 反代服务
	ProxyIDs  []string `json:"proxy_ids,omitempty"`  // 转发出口代理（从代理池选择）
	Builtin   bool     `json:"builtin,omitempty"`    // 默认分组不可删
	CreatedAt string   `json:"created_at"`
}

// GatewayAPIKey API 密钥：外部客户端用 Bearer sk-xxx 调本地网关。
type GatewayAPIKey struct {
	ID        string `json:"id"`
	Key       string `json:"key"`         // sk-xxx（列表接口脱敏）
	KeyFull   string `json:"key_full,omitempty"` // 仅在创建响应中返回一次
	Name      string `json:"name"`
	GroupID   string `json:"group_id"`
	Status    string `json:"status"` // active | disabled
	CreatedAt string `json:"created_at"`
}

// UpstreamService 上游服务（sub2api channel）：定义转发目标。
// Type=grok 时按 grok 反代身份头规则转发（cli-chat-proxy.grok.com）；
// openai/anthropic 为自定义 OpenAI/Anthropic 兼容上游（透传身份头）。
type UpstreamService struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // grok | openai | anthropic
	BaseURL   string `json:"base_url"`
	Enabled   bool   `json:"enabled"`
	Builtin   bool   `json:"builtin,omitempty"` // 内置 grok 反代不可删
	CreatedAt string `json:"created_at"`
}

// GatewayConfig 网关配置。
type GatewayConfig struct {
	Enabled bool `json:"enabled"` // 默认 true：随 app 启动监听
	Port    int  `json:"port"`    // 默认 18789
	// ListenHost 监听地址：0.0.0.0 = 内外网均可访问（默认，需 Key 认证）；
	// 127.0.0.1 = 仅本机。空值兼容旧数据 = 0.0.0.0。
	ListenHost string `json:"listen_host,omitempty"`
}

// 存储 key（settings.json 顶层）。
const (
	KeyGroups   = "gateway_groups"
	KeyKeys     = "gateway_keys"
	KeyServices = "gateway_services"
	KeyConfig   = "gateway_config"

	DefaultPort = 18789
	// DefaultListenHost 默认监听所有网卡（内外网可访问，靠 API Key 认证）。
	DefaultListenHost = "0.0.0.0"

	DefaultServiceID = "grok-official"
	DefaultGroupID   = "default"

	KeyStatusActive   = "active"
	KeyStatusDisabled = "disabled"
)

// DefaultGrokBaseURL grok 官方 CLI 聊天网关（免费 OAuth 账号的唯一可用上游：
// 走 api.x.ai 会被判「无 API 额度」402）。
const DefaultGrokBaseURL = "https://cli-chat-proxy.grok.com/v1"
