package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"crypto/rand"
)

const (
	KeyWarpCustomKeys   = "warp_custom_keys"
	KeyWarpSelectedKey  = "warp_selected_key"
	KeyWarpKeyMode      = "warp_key_mode" // custom | catalog
	warpKeyCatalogURL   = "https://warpkey-edge-console.niubiplus.workers.dev/api/full"
	warpKeyCatalogLite  = "https://warpkey-edge-console.niubiplus.workers.dev/api/lite"
	cfClientAPI         = "https://api.cloudflareclient.com/v0a2158"
)

// WarpKeyInfo is one license entry for UI dropdown.
type WarpKeyInfo struct {
	Key           string `json:"key"`
	Source        string `json:"source"` // public | custom
	Label         string `json:"label"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	AccountType   string `json:"account_type,omitempty"`
	WarpPlus      bool   `json:"warp_plus"`
	Quota         int64  `json:"quota"`          // bytes
	PremiumData   int64  `json:"premium_data"`   // bytes
	Usage         int64  `json:"usage"`          // bytes if present
	DeviceCount   int    `json:"device_count"`   // -1 unknown
	DeviceLimit   int    `json:"device_limit"`   // typically 5 for plus shared
	ReferralCount int    `json:"referral_count"`
	QuotaHuman    string `json:"quota_human"`
	PremiumHuman  string `json:"premium_human"`
	UsageHuman    string `json:"usage_human"`
	ProbedAt      string `json:"probed_at,omitempty"`
}

type WarpKeyCatalog struct {
	Mode         string        `json:"mode"` // custom | catalog
	SelectedKey  string        `json:"selected_key"`
	CustomKeys   []string      `json:"custom_keys"`
	PublicKeys   []WarpKeyInfo `json:"public_keys"`
	CustomInfos  []WarpKeyInfo `json:"custom_infos"`
	HasLicense   bool          `json:"has_license"`
	SourceStatus string        `json:"source_status"`
	UpdatedAt    string        `json:"updated_at"`
	Hint         string        `json:"hint"`
}

type keyProbeCache struct {
	mu    sync.Mutex
	items map[string]WarpKeyInfo
	at    time.Time
}

var globalKeyProbe = &keyProbeCache{items: map[string]WarpKeyInfo{}}

func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

func (w *WarpService) GetKeyCatalog(ctx context.Context, probe bool) (WarpKeyCatalog, error) {
	selected, _ := w.GetLicense(ctx)
	// public shared keys removed: only user custom keys
	mode, _ := w.store.GetValue(ctx, KeyWarpKeyMode)
	if strings.TrimSpace(selected) == "" {
		mode = "free"
	} else if strings.TrimSpace(mode) == "" || mode == "catalog" {
		mode = "custom"
	}
	custom := w.loadCustomKeys(ctx)
	// do not auto-inject selected into custom list (avoids sticky junk keys in dropdown)
	out := WarpKeyCatalog{
		Mode:         mode,
		SelectedKey:  selected,
		CustomKeys:   custom,
		PublicKeys:   []WarpKeyInfo{},
		HasLicense:   strings.TrimSpace(selected) != "",
		SourceStatus: "public_disabled",
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Hint:         "默认免费 WARP；填写自己的 Warp+ Key 后自动保存并启用 Plus。公共共享 Key 已移除。",
	}

	probed := 0
	const maxSyncProbe = 6
	for _, k := range custom {
		info := WarpKeyInfo{
			Key: k, Source: "custom", Label: maskKey(k),
			DeviceCount: -1, DeviceLimit: 5,
		}
		if probe && probed < maxSyncProbe {
			info = probeWarpKey(k, "custom")
			probed++
		} else if cached, ok := getCachedProbe(k); ok {
			info = cached
			info.Source = "custom"
		}
		out.CustomInfos = append(out.CustomInfos, info)
	}

	if !probe {
		go softProbeKeys(append([]string{}, custom...))
	}
	return out, nil
}

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 12 {
		return k
	}
	return k[:6] + "…" + k[len(k)-4:]
}

func (w *WarpService) loadCustomKeys(ctx context.Context) []string {
	raw, _ := w.store.GetValue(ctx, KeyWarpCustomKeys)
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		// also accept newline separated
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				keys = append(keys, line)
			}
		}
		return keys
	}
	out := make([]string, 0, len(keys))
	seen := map[string]struct{}{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || strings.Count(k, "-") < 2 || len(k) < 16 {
			continue
		}
		// drop known test junk
		up := strings.ToUpper(k)
		if strings.Contains(up, "TEST") || strings.Contains(up, "DEMO") || strings.Contains(up, "AABBCCDD") {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// AutoSaveKeySelection persists mode + selected key + optional new custom key.
// mode: "custom" | "catalog"
func (w *WarpService) AutoSaveKeySelection(ctx context.Context, mode, key string, rememberCustom bool) (WarpKeyCatalog, error) {
	mode = strings.TrimSpace(mode)
	key = strings.TrimSpace(key)
	// free / empty key => no license (free WARP). only non-empty key enables Warp+.
	if mode == "free" || mode == "catalog" || key == "" {
		mode = "free"
		key = ""
		rememberCustom = false
	} else {
		mode = "custom"
	}
	_ = w.store.Set(ctx, KeyWarpKeyMode, mode)
	_ = w.SaveLicense(ctx, key)

	if rememberCustom && key != "" {
		list := w.loadCustomKeys(ctx)
		found := false
		for _, k := range list {
			if k == key {
				found = true
				break
			}
		}
		if !found {
			list = append([]string{key}, list...)
			if len(list) > 30 {
				list = list[:30]
			}
			b, _ := json.Marshal(list)
			_ = w.store.Set(ctx, KeyWarpCustomKeys, string(b))
		}
	}
	// invalidate nothing critical; return catalog without full probe
	return w.GetKeyCatalog(ctx, false)
}

func (w *WarpService) ProbeKey(ctx context.Context, key string) (WarpKeyInfo, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return WarpKeyInfo{}, fmt.Errorf("empty key")
	}
	info := probeWarpKey(key, "custom")
	return info, nil
}

func fetchPublicWarpKeys() ([]string, string) {
	// Public shared key catalogs disabled — they are almost always device-full / unusable.
	return []string{}, "public_disabled"
}

func normalizeKeyList(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.TrimSpace(k)
		k = strings.Trim(k, "`\"'")
		if k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		// license format typically 3 segments
		if strings.Count(k, "-") < 2 || len(k) < 16 {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
		if len(out) >= 40 {
			break
		}
	}
	return out
}

func getCachedProbe(key string) (WarpKeyInfo, bool) {
	globalKeyProbe.mu.Lock()
	defer globalKeyProbe.mu.Unlock()
	it, ok := globalKeyProbe.items[key]
	if !ok {
		return WarpKeyInfo{}, false
	}
	// cache 10 minutes
	if time.Since(globalKeyProbe.at) > 10*time.Minute && time.Since(mustParseTime(it.ProbedAt)) > 10*time.Minute {
		return it, true // still return stale but mark as cache
	}
	return it, true
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func setCachedProbe(info WarpKeyInfo) {
	globalKeyProbe.mu.Lock()
	defer globalKeyProbe.mu.Unlock()
	if globalKeyProbe.items == nil {
		globalKeyProbe.items = map[string]WarpKeyInfo{}
	}
	globalKeyProbe.items[info.Key] = info
	globalKeyProbe.at = time.Now()
}

func softProbeKeys(keys []string) {
	// probe up to 8 keys to keep UI responsive
	n := 0
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := getCachedProbe(k); ok {
			continue
		}
		_ = probeWarpKey(k, "public")
		n++
		if n >= 8 {
			return
		}
	}
}

func probeWarpKey(key, source string) WarpKeyInfo {
	info := WarpKeyInfo{
		Key: key, Source: source, Label: maskKey(key),
		DeviceCount: -1, DeviceLimit: 5,
		ProbedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if cached, ok := getCachedProbe(key); ok {
		// refresh if younger than 3 min
		if time.Since(mustParseTime(cached.ProbedAt)) < 3*time.Minute {
			cached.Source = source
			return cached
		}
	}

	// 1) register ephemeral device
	installID := randomUUID()
	pub := randomWGKey()
	regBody := map[string]any{
		"key":        pub,
		"install_id": installID,
		"fcm_token":  installID,
		"tos":        time.Now().UTC().Format("2006-01-02T15:04:05.000+00:00"),
		"model":      "PC",
		"type":       "Android",
		"locale":     "en_US",
	}
	st, reg, errBody := cfJSON(http.MethodPost, cfClientAPI+"/reg", regBody, "")
	if st != 200 {
		info.Error = fmt.Sprintf("reg_fail:%d %s", st, truncate(errBody, 80))
		setCachedProbe(info)
		return info
	}
	token, _ := reg["token"].(string)
	did, _ := reg["id"].(string)
	if token == "" || did == "" {
		info.Error = "reg_missing_token"
		setCachedProbe(info)
		return info
	}
	// always cleanup
	defer func() {
		_, _, _ = cfJSON(http.MethodDelete, cfClientAPI+"/reg/"+did, nil, token)
	}()

	// 2) attach license
	st, acc, errBody := cfJSON(http.MethodPut, cfClientAPI+"/reg/"+did+"/account", map[string]any{"license": key}, token)
	if st != 200 {
		msg := extractCFError(errBody)
		if msg == "" {
			msg = truncate(errBody, 120)
		}
		info.Error = msg
		// still try to mark device-full specially
		if strings.Contains(strings.ToLower(msg), "too many") {
                        info.OK = false
                        info.WarpPlus = true
                        info.DeviceCount = info.DeviceLimit
                        info.QuotaHuman = "设备已满"
                        info.PremiumHuman = "设备已满"
                }
		setCachedProbe(info)
		return info
	}

	info.OK = true
	info.AccountType, _ = acc["account_type"].(string)
	info.WarpPlus, _ = acc["warp_plus"].(bool)
	info.Quota = jsonInt64(acc["quota"])
	info.PremiumData = jsonInt64(acc["premium_data"])
	info.Usage = jsonInt64(acc["usage"])
	info.ReferralCount = int(jsonInt64(acc["referral_count"]))
	info.QuotaHuman = humanBytes(info.Quota)
	info.PremiumHuman = humanBytes(info.PremiumData)
	info.UsageHuman = humanBytes(info.Usage)

	// 3) devices list
	st, devRaw, _ := cfJSON(http.MethodGet, cfClientAPI+"/reg/"+did+"/account/devices", nil, token)
	if st == 200 {
		// devices may come as array at top-level — cfJSON puts array under "_array"
		if arr, ok := devRaw["_array"].([]any); ok {
			info.DeviceCount = len(arr)
		} else if arr, ok := asAnySlice(devRaw); ok {
			info.DeviceCount = len(arr)
		}
	}

	// unlimited premium often shows huge quota or warp_plus with 0 quota meaning unlimited in some builds
	if info.PremiumData == 0 && info.Quota == 0 && info.WarpPlus {
		info.PremiumHuman = "Plus / 未知额度"
		info.QuotaHuman = "—"
	}
	setCachedProbe(info)
	return info
}

func asAnySlice(m map[string]any) ([]any, bool) {
	// not a slice
	return nil, false
}

func jsonInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	default:
		return 0
	}
}

func extractCFError(body string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	errs, _ := m["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	e0, _ := errs[0].(map[string]any)
	msg, _ := e0["message"].(string)
	return msg
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomWGKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func cfJSON(method, url string, body any, token string) (int, map[string]any, string) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err.Error()
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("CF-Client-Version", "a-6.30-3596")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if len(raw) == 0 {
		return resp.StatusCode, map[string]any{}, ""
	}
	// array response
	var arr []any
	if json.Unmarshal(raw, &arr) == nil {
		return resp.StatusCode, map[string]any{"_array": arr}, string(raw)
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return resp.StatusCode, map[string]any{}, string(raw)
	}
	return resp.StatusCode, m, string(raw)
}
