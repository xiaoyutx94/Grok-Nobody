package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/umbraforge/desktop/internal/store"
)

// randomHex 返回 n 字节的随机十六进制串（密钥生成用，非 Math.random）。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 极低概率失败：回退时间戳哈希保证不 panic
		return strings.ReplaceAll(fmt.Sprintf("%x", time.Now().UnixNano()), "0", "f")
	}
	return hex.EncodeToString(b)
}

// GatewayStore 网关数据的 JSONStore 封装（单实例锁，与 AccountService 同模式）。
type GatewayStore struct {
	st *store.JSONStore
	mu sync.Mutex
}

func NewGatewayStore(st *store.JSONStore) *GatewayStore {
	return &GatewayStore{st: st}
}

// ensureDefaults 幂等初始化：内置 grok 反代服务 + 默认分组 + 默认配置。
// 已有数据绝不覆盖（升级兼容）。
func (s *GatewayStore) ensureDefaults(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)

	// 服务
	svcs, _ := s.listServices(ctx)
	if len(svcs) == 0 {
		svcs = []UpstreamService{{
			ID:        DefaultServiceID,
			Name:      "Grok 官方反代",
			Type:      "grok",
			BaseURL:   DefaultGrokBaseURL,
			Enabled:   true,
			Builtin:   true,
			CreatedAt: now,
		}}
		if err := s.save(ctx, KeyServices, svcs); err != nil {
			return err
		}
	}

	// 分组
	groups, _ := s.listGroups(ctx)
	if len(groups) == 0 {
		groups = []GatewayGroup{{
			ID:        DefaultGroupID,
			Name:      "默认分组",
			Desc:      "默认账号分组（绑定内置 Grok 官方反代）",
			ServiceID: DefaultServiceID,
			Builtin:   true,
			CreatedAt: now,
		}}
		if err := s.save(ctx, KeyGroups, groups); err != nil {
			return err
		}
	}

	// 配置（直接 load，避免 GetConfig 的锁在 ensureDefaults 持锁时重入死锁）
	var cfg GatewayConfig
	if err := s.load(ctx, KeyConfig, &cfg); err != nil || cfg.Port <= 0 || cfg.Port > 65535 {
		if err := s.save(ctx, KeyConfig, GatewayConfig{Enabled: true, Port: DefaultPort}); err != nil {
			return err
		}
	}
	return nil
}

// EnsureDefaults 幂等初始化入口（Start 时调用）。
func (s *GatewayStore) EnsureDefaults(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureDefaults(ctx)
}

// ---------- 通用 ----------

func (s *GatewayStore) load(ctx context.Context, key string, out any) error {
	raw, err := s.st.GetValue(ctx, key)
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func (s *GatewayStore) save(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.st.Set(ctx, key, string(b))
}

// ---------- 服务 ----------

func (s *GatewayStore) listServices(ctx context.Context) ([]UpstreamService, error) {
	var list []UpstreamService
	if err := s.load(ctx, KeyServices, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ListServices 服务列表。
func (s *GatewayStore) ListServices(ctx context.Context) ([]UpstreamService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listServices(ctx)
}

// GetService 按 ID 取服务；不存在返回错误。
func (s *GatewayStore) GetService(ctx context.Context, id string) (UpstreamService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		id = DefaultServiceID
	}
	svcs, err := s.listServices(ctx)
	if err != nil {
		return UpstreamService{}, err
	}
	for _, svc := range svcs {
		if svc.ID == id {
			return svc, nil
		}
	}
	return UpstreamService{}, fmt.Errorf("服务不存在: %s", id)
}

// CreateService 添加服务（type: grok|openai|anthropic）。
func (s *GatewayStore) CreateService(ctx context.Context, name, typ, baseURL string, enabled bool) (UpstreamService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svcs, err := s.listServices(ctx)
	if err != nil {
		return UpstreamService{}, err
	}
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		typ = "openai"
	}
	if typ != "grok" && typ != "openai" && typ != "anthropic" {
		return UpstreamService{}, fmt.Errorf("服务类型无效: %s（grok|openai|anthropic）", typ)
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return UpstreamService{}, fmt.Errorf("base_url 不能为空")
	}
	svc := UpstreamService{
		ID:        uuid.NewString(),
		Name:      strings.TrimSpace(name),
		Type:      typ,
		BaseURL:   baseURL,
		Enabled:   enabled,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if svc.Name == "" {
		svc.Name = svc.ID[:8]
	}
	svcs = append(svcs, svc)
	if err := s.save(ctx, KeyServices, svcs); err != nil {
		return UpstreamService{}, err
	}
	return svc, nil
}

// UpdateService 更新服务（内置 grok 反代禁止改 Type/BaseURL，仅可启停）。
func (s *GatewayStore) UpdateService(ctx context.Context, id string, patch map[string]any) (UpstreamService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svcs, err := s.listServices(ctx)
	if err != nil {
		return UpstreamService{}, err
	}
	for i := range svcs {
		if svcs[i].ID != id {
			continue
		}
		if svcs[i].Builtin {
			if _, ok := patch["type"]; ok {
				return UpstreamService{}, fmt.Errorf("内置 grok 反代服务不可修改类型")
			}
			if _, ok := patch["base_url"]; ok {
				return UpstreamService{}, fmt.Errorf("内置 grok 反代服务不可修改地址")
			}
		}
		if v, ok := patch["name"].(string); ok {
			svcs[i].Name = strings.TrimSpace(v)
		}
		if v, ok := patch["enabled"].(bool); ok {
			svcs[i].Enabled = v
		}
		if err := s.save(ctx, KeyServices, svcs); err != nil {
			return UpstreamService{}, err
		}
		return svcs[i], nil
	}
	return UpstreamService{}, fmt.Errorf("服务不存在: %s", id)
}

// DeleteService 删除自定义服务；内置服务拒绝删除。分组仍引用该服务时拒绝。
func (s *GatewayStore) DeleteService(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	svcs, err := s.listServices(ctx)
	if err != nil {
		return err
	}
	groups, err := s.listGroups(ctx)
	if err != nil {
		return err
	}
	for i := range svcs {
		if svcs[i].ID != id {
			continue
		}
		if svcs[i].Builtin {
			return fmt.Errorf("内置 grok 反代服务不可删除（可禁用）")
		}
		for _, g := range groups {
			if g.ServiceID == id {
				return fmt.Errorf("分组 %q 仍引用该服务，请先解绑", g.Name)
			}
		}
		svcs = append(svcs[:i], svcs[i+1:]...)
		return s.save(ctx, KeyServices, svcs)
	}
	return fmt.Errorf("服务不存在: %s", id)
}

// ---------- 分组 ----------

func (s *GatewayStore) listGroups(ctx context.Context) ([]GatewayGroup, error) {
	var list []GatewayGroup
	if err := s.load(ctx, KeyGroups, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ListGroups 分组列表。
func (s *GatewayStore) ListGroups(ctx context.Context) ([]GatewayGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listGroups(ctx)
}

// GetGroup 按 ID 取分组；不存在返回错误。
func (s *GatewayStore) GetGroup(ctx context.Context, id string) (GatewayGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getGroupLocked(ctx, id)
}

// CreateGroup 创建分组。
func (s *GatewayStore) CreateGroup(ctx context.Context, name, desc, serviceID string, proxyIDs []string) (GatewayGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.listGroups(ctx)
	if err != nil {
		return GatewayGroup{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return GatewayGroup{}, fmt.Errorf("分组名称不能为空")
	}
	for _, g := range groups {
		if strings.EqualFold(g.Name, name) {
			return GatewayGroup{}, fmt.Errorf("分组名称已存在: %s", name)
		}
	}
	if serviceID == "" {
		serviceID = DefaultServiceID
	}
	g := GatewayGroup{
		ID:        uuid.NewString(),
		Name:      name,
		Desc:      strings.TrimSpace(desc),
		ServiceID: serviceID,
		ProxyIDs:  normalizeProxyIDs(proxyIDs),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	groups = append(groups, g)
	if err := s.save(ctx, KeyGroups, groups); err != nil {
		return GatewayGroup{}, err
	}
	return g, nil
}

// UpdateGroup 更新分组（名称/描述/服务/代理绑定）。
func (s *GatewayStore) UpdateGroup(ctx context.Context, id string, patch map[string]any) (GatewayGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.listGroups(ctx)
	if err != nil {
		return GatewayGroup{}, err
	}
	for i := range groups {
		if groups[i].ID != id {
			continue
		}
		if v, ok := patch["name"].(string); ok && strings.TrimSpace(v) != "" {
			groups[i].Name = strings.TrimSpace(v)
		}
		if v, ok := patch["desc"].(string); ok {
			groups[i].Desc = strings.TrimSpace(v)
		}
		if v, ok := patch["service_id"].(string); ok {
			groups[i].ServiceID = strings.TrimSpace(v)
		}
		if v, ok := patch["proxy_ids"].([]any); ok {
			ids := make([]string, 0, len(v))
			for _, x := range v {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					ids = append(ids, strings.TrimSpace(s))
				}
			}
			groups[i].ProxyIDs = normalizeProxyIDs(ids)
		}
		if err := s.save(ctx, KeyGroups, groups); err != nil {
			return GatewayGroup{}, err
		}
		return groups[i], nil
	}
	return GatewayGroup{}, fmt.Errorf("分组不存在: %s", id)
}

// DeleteGroup 删除分组（内置默认分组拒绝；组内有关键 Key 时拒绝）。
func (s *GatewayStore) DeleteGroup(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.listGroups(ctx)
	if err != nil {
		return err
	}
	keys, err := s.listKeys(ctx)
	if err != nil {
		return err
	}
	for i := range groups {
		if groups[i].ID != id {
			continue
		}
		if groups[i].Builtin {
			return fmt.Errorf("默认分组不可删除")
		}
		for _, k := range keys {
			if k.GroupID == id {
				return fmt.Errorf("分组下仍有 API Key，请先删除或迁移 Key")
			}
		}
		groups = append(groups[:i], groups[i+1:]...)
		return s.save(ctx, KeyGroups, groups)
	}
	return fmt.Errorf("分组不存在: %s", id)
}

func normalizeProxyIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ---------- API Key ----------

func (s *GatewayStore) listKeys(ctx context.Context) ([]GatewayAPIKey, error) {
	var list []GatewayAPIKey
	if err := s.load(ctx, KeyKeys, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ListKeys 密钥列表（Key 字段脱敏为 sk-<前6>…）。
func (s *GatewayStore) ListKeys(ctx context.Context) ([]GatewayAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listKeys(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Key = maskKey(list[i].Key)
	}
	return list, nil
}

// CreateKey 生成密钥（sk- + 32 hex），返回完整 key 一次。
func (s *GatewayStore) CreateKey(ctx context.Context, name, groupID string) (GatewayAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.listKeys(ctx)
	if err != nil {
		return GatewayAPIKey{}, err
	}
	// 校验分组存在
	if _, err := s.getGroupLocked(ctx, groupID); err != nil {
		return GatewayAPIKey{}, err
	}
	k := GatewayAPIKey{
		ID:        uuid.NewString(),
		Key:       "sk-" + randomHex(32),
		Name:      strings.TrimSpace(name),
		GroupID:   groupID,
		Status:    KeyStatusActive,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if k.Name == "" {
		k.Name = "key-" + k.ID[:8]
	}
	keys = append(keys, k)
	if err := s.save(ctx, KeyKeys, keys); err != nil {
		return GatewayAPIKey{}, err
	}
	k.KeyFull = k.Key
	k.Key = maskKey(k.Key)
	return k, nil
}

// GetKeyBySecret 认证用：按完整密钥查（含状态校验）。
func (s *GatewayStore) GetKeyBySecret(ctx context.Context, secret string) (GatewayAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.listKeys(ctx)
	if err != nil {
		return GatewayAPIKey{}, err
	}
	for _, k := range keys {
		if k.Key == secret {
			if k.Status != KeyStatusActive {
				return GatewayAPIKey{}, fmt.Errorf("密钥已禁用")
			}
			return k, nil
		}
	}
	return GatewayAPIKey{}, fmt.Errorf("无效密钥")
}

// GetKeyFull 按 id 返回完整密钥（复制用；禁用状态也返回）。
func (s *GatewayStore) GetKeyFull(ctx context.Context, id string) (GatewayAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.listKeys(ctx)
	if err != nil {
		return GatewayAPIKey{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			k.KeyFull = k.Key
			k.Key = maskKey(k.Key)
			return k, nil
		}
	}
	return GatewayAPIKey{}, fmt.Errorf("密钥不存在: %s", id)
}

// SetKeyStatus 启用/禁用密钥。
func (s *GatewayStore) SetKeyStatus(ctx context.Context, id, status string) (GatewayAPIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.listKeys(ctx)
	if err != nil {
		return GatewayAPIKey{}, err
	}
	for i := range keys {
		if keys[i].ID != id {
			continue
		}
		keys[i].Status = status
		if err := s.save(ctx, KeyKeys, keys); err != nil {
			return GatewayAPIKey{}, err
		}
		keys[i].Key = maskKey(keys[i].Key)
		return keys[i], nil
	}
	return GatewayAPIKey{}, fmt.Errorf("密钥不存在: %s", id)
}

// DeleteKey 删除密钥。
func (s *GatewayStore) DeleteKey(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.listKeys(ctx)
	if err != nil {
		return err
	}
	for i := range keys {
		if keys[i].ID == id {
			keys = append(keys[:i], keys[i+1:]...)
			return s.save(ctx, KeyKeys, keys)
		}
	}
	return fmt.Errorf("密钥不存在: %s", id)
}

func (s *GatewayStore) getGroupLocked(ctx context.Context, id string) (GatewayGroup, error) {
	if id == "" {
		id = DefaultGroupID
	}
	groups, err := s.listGroups(ctx)
	if err != nil {
		return GatewayGroup{}, err
	}
	for _, g := range groups {
		if g.ID == id {
			return g, nil
		}
	}
	return GatewayGroup{}, fmt.Errorf("分组不存在: %s", id)
}

// ---------- 配置 ----------

// GetConfig 网关配置（缺失时回落默认值并落盘）。
func (s *GatewayStore) GetConfig(ctx context.Context) (GatewayConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cfg GatewayConfig
	if err := s.load(ctx, KeyConfig, &cfg); err != nil {
		return GatewayConfig{Enabled: true, Port: DefaultPort, ListenHost: DefaultListenHost}, nil
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = DefaultPort
	}
	if strings.TrimSpace(cfg.ListenHost) == "" {
		cfg.ListenHost = DefaultListenHost
	}
	return cfg, nil
}

// SetConfig 保存配置（端口 1-65535 校验；listen_host 仅允许 0.0.0.0/127.0.0.1）。
func (s *GatewayStore) SetConfig(ctx context.Context, cfg GatewayConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("端口无效: %d", cfg.Port)
	}
	host := strings.TrimSpace(cfg.ListenHost)
	if host == "" {
		host = DefaultListenHost
	}
	if host != "0.0.0.0" && host != "127.0.0.1" {
		return fmt.Errorf("listen_host 仅支持 0.0.0.0（内外网）或 127.0.0.1（仅本机）")
	}
	cfg.ListenHost = host
	return s.save(ctx, KeyConfig, cfg)
}

func maskKey(k string) string {
	if len(k) <= 10 {
		return "sk-***"
	}
	return k[:9] + "…"
}
