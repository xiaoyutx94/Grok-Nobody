package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/umbraforge/desktop/internal/procutil"
	"github.com/umbraforge/desktop/internal/store"
)

// ── iCloud Privacy Mail 取码平台网关 ──
//
// 平台：https://github.com/q1953258942/iCloud-Privacy-Mail
// 功能：平台账号登录（持 cookie）、账号/邮箱列表、创建 Hide My Email
// 随机子邮箱、把平台邮箱同步成本地邮箱池（注册页按账号选择）。
//
// 平台会话 cookie 只保存在内存（不落盘），重启后需重新登录；
// 本地邮箱池落盘（store key icloud_mailboxes），注册链路消费。

// IcloudMailboxEntry 本地池条目。
type IcloudMailboxEntry struct {
	ID            string `json:"id,omitempty"` // 平台邮箱 ID（mbx_xxx），删除用
	Email         string `json:"email"`
	APIURL        string `json:"api_url"` // 取码完整地址（含 mailbox_key）
	AccountID     string `json:"account_id"`
	AccountLabel  string `json:"account_label"`
	Status        string `json:"status"` // available | used | disabled
	ClaimedAt     string `json:"claimed_at,omitempty"`
}

// IcloudPool 本地邮箱池（store key）。
type IcloudPool struct {
	Entries []IcloudMailboxEntry `json:"entries"`
}

// IcloudAccount 平台账号（注册页按账号选择）。
type IcloudAccount struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	AppleID string `json:"apple_id"`
	IsAdmin bool   `json:"is_admin"`
}

// IcloudMailbox 平台邮箱（列表/导出）。
type IcloudMailbox struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	AccountID string `json:"account_id"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	APIURL    string `json:"api_url"`
	Used      bool   `json:"used"`
}

// IcloudGateway 平台网关。
type IcloudGateway struct {
	mu     sync.Mutex
	store  *store.JSONStore
	client *http.Client
	// baseURL → 会话 cookie（内存态，不落盘）
	sessions map[string][]*http.Cookie
	loggedIn map[string]bool
	// 内置平台服务进程（方案 B：桌面版内嵌 iCloud-Privacy-Mail）
	panelCmd    *exec.Cmd
	panelPort   int
	panelBin    string
	panelData   string
	panelCreds  string // 自动注册的账号 username:password（内存）
	panelRunDir string
}

const icloudPoolKey = "icloud_mailboxes"

// 内置平台固定内部账号（本地 127.0.0.1 服务，凭据仅存内存；
// 固定值保证 GUI/headless 共用同一会话域，Apple 登录态互通）。
const (
	icloudPanelUser = "umbraforge-local"
	icloudPanelPass = "umbraforge-local-pass"
)

func NewIcloudGateway(st *store.JSONStore) *IcloudGateway {
	return &IcloudGateway{
		store:       st,
		client:      &http.Client{Timeout: 40 * time.Second},
		sessions:    map[string][]*http.Cookie{},
		loggedIn:    map[string]bool{},
		panelRunDir: st.Dir(),
	}
}

// SetPanelRunDir 设置内置平台数据目录（默认跟随 store 数据目录）。
func (g *IcloudGateway) SetPanelRunDir(dir string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if strings.TrimSpace(dir) != "" {
		g.panelRunDir = dir
	}
}

func normBase(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// Login 平台账号密码登录，成功后保存会话 cookie（内存）。
func (g *IcloudGateway) Login(ctx context.Context, baseURL, username, password string) error {
	base := normBase(baseURL)
	if base == "" || username == "" || password == "" {
		return fmt.Errorf("需要平台地址、账号与密码")
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/auth/login", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("平台登录请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Success bool `json:"success"`
		Error   *struct{ Message string `json:"message"` } `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Success {
		msg := "平台登录失败"
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return fmt.Errorf("%s", msg)
	}
	g.mu.Lock()
	g.sessions[base] = resp.Cookies()
	g.loggedIn[base] = true
	g.mu.Unlock()
	return nil
}

// LoggedIn 会话是否已建立。
func (g *IcloudGateway) LoggedIn(baseURL string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.loggedIn[normBase(baseURL)]
}

// Logout 清除会话。
func (g *IcloudGateway) Logout(baseURL string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := normBase(baseURL)
	delete(g.sessions, b)
	delete(g.loggedIn, b)
}

// do 带会话 cookie 请求平台（需要登录的接口）。
func (g *IcloudGateway) do(ctx context.Context, baseURL, method, path string, body any) ([]byte, int, error) {
	base := normBase(baseURL)
	g.mu.Lock()
	cookies := append([]*http.Cookie(nil), g.sessions[base]...)
	g.mu.Unlock()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("平台请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}

// Accounts 平台账号列表。
func (g *IcloudGateway) Accounts(ctx context.Context, baseURL string) ([]IcloudAccount, error) {
	raw, code, err := g.do(ctx, baseURL, http.MethodGet, "/api/accounts", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized {
		return nil, fmt.Errorf("平台会话已失效，请重新登录")
	}
	var out struct {
		Success  bool            `json:"success"`
		Accounts []IcloudAccount `json:"accounts"`
		Error    *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("账号列表解析失败: %w", err)
	}
	if !out.Success {
		msg := "获取账号失败"
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out.Accounts, nil
}

// Mailboxes 平台邮箱列表（含取码地址）。
func (g *IcloudGateway) Mailboxes(ctx context.Context, baseURL string) ([]IcloudMailbox, error) {
	raw, code, err := g.do(ctx, baseURL, http.MethodGet, "/api/mailboxes", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized {
		return nil, fmt.Errorf("平台会话已失效，请重新登录")
	}
	var out struct {
		Success   bool            `json:"success"`
		Mailboxes []IcloudMailbox `json:"mailboxes"`
		Error     *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("邮箱列表解析失败: %w", err)
	}
	if !out.Success {
		msg := "获取邮箱失败"
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out.Mailboxes, nil
}

// CreateMailboxes 在平台创建 Hide My Email 随机子邮箱。
// accountID 为空 = 全部账号（先拉账号列表再按 account_ids 传）；
// 平台一次请求 = 勾选账号各创建 1 个，count>1 时循环调用累计。
func (g *IcloudGateway) CreateMailboxes(ctx context.Context, baseURL string, accountID string, count int) (int, error) {
	if count < 1 {
		count = 1
	}
	// 空 account_id：拉全部账号，逗号拼接（平台按分隔符解析）
	sel := strings.TrimSpace(accountID)
	if sel == "" {
		accs, err := g.Accounts(ctx, baseURL)
		if err != nil {
			return 0, fmt.Errorf("获取账号列表失败（创建邮箱需要账号）: %w", err)
		}
		ids := make([]string, 0, len(accs))
		for _, a := range accs {
			ids = append(ids, a.ID)
		}
		sel = strings.Join(ids, ",")
		if sel == "" {
			return 0, fmt.Errorf("平台没有可用账号，请先在平台保存 Apple 登录态")
		}
	}
	total := 0
	for i := 0; i < count; i++ {
		payload := map[string]any{"account_id": sel}
		raw, code, err := g.do(ctx, baseURL, http.MethodPost, "/api/icloud/mailboxes/create", payload)
		if err != nil {
			return total, err
		}
		if code == http.StatusUnauthorized {
			return total, fmt.Errorf("平台会话已失效，请重新登录")
		}
		// 平台 create 响应无 success 字段：{created:N, mailbox:{...}, failures:[...]}
		// ——成功判断用 created>0；failures 里有内容时报出具体原因
		var out struct {
			Created  int `json:"created"`
			Failures []struct {
				AccountID string `json:"account_id"`
				Error     string `json:"error"`
			} `json:"failures"`
			Error *struct{ Message string `json:"message"` } `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return total, fmt.Errorf("创建邮箱响应解析失败: %w", err)
		}
		if out.Created > 0 {
			total += out.Created
			continue
		}
		if len(out.Failures) > 0 {
			f := out.Failures[0]
			msg := f.Error
			if msg == "" {
				msg = fmt.Sprintf("%s 创建失败", f.AccountID)
			}
			return total, fmt.Errorf("创建邮箱失败: %s", msg)
		}
		if out.Error != nil && out.Error.Message != "" {
			return total, fmt.Errorf("创建邮箱失败: %s", out.Error.Message)
		}
		return total, fmt.Errorf("创建邮箱失败（平台未返回创建结果，HTTP %d）", code)
	}
	return total, nil
}

// ── 内置平台服务（方案 B）──

// PanelStatus 内置服务状态。
type PanelStatus struct {
	Running  bool   `json:"running"`
	URL      string `json:"url,omitempty"`
	Port     int    `json:"port,omitempty"`
	Health   string `json:"health,omitempty"`
	DataDir  string `json:"data_dir,omitempty"`
}

// PanelURL 内置服务地址（未启动返回空）。
func (g *IcloudGateway) PanelURL() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.panelPort > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", g.panelPort)
	}
	return ""
}

// PoolEntryByEmail 按邮箱找本地池条目。
func (g *IcloudGateway) PoolEntryByEmail(email string) *IcloudMailboxEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.loadPool()
	for i := range p.Entries {
		if p.Entries[i].Email == email {
			return &p.Entries[i]
		}
	}
	return nil
}

// PanelStatus 返回内置服务状态。
func (g *IcloudGateway) PanelStatus() PanelStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := PanelStatus{Running: g.panelCmd != nil, DataDir: g.panelData}
	if g.panelPort > 0 {
		st.Port = g.panelPort
		st.URL = fmt.Sprintf("http://127.0.0.1:%d", g.panelPort)
	}
	if st.Running {
		st.Health = "starting"
	}
	return st
}

// HasPanelData 平台数据目录是否存在（Apple 登录态/邮箱池）——
// 注册发起时据此自动启动内置服务。用启动时固定的 panelRunDir
// 检查磁盘（panelData 只在 StartPanel 后才有值）。
func (g *IcloudGateway) HasPanelData() bool {
	g.mu.Lock()
	dir := g.panelRunDir
	g.mu.Unlock()
	if dir == "" {
		return false
	}
	return fileExists(filepath.Join(dir, "icloud-panel", "data", "state.json"))
}

// PanelPort 当前内置服务端口（未启动返回 0）。
func (g *IcloudGateway) PanelPort() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.panelPort
}

// probePanelBin 自动探测内置平台二进制：
// exe 同级 plugins/icloud-panel/（Windows）或 Contents/Resources/plugins/icloud-panel/（macOS app）。
func probePanelBin() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	base := filepath.Dir(exe)
	var cands []string
	if runtime.GOOS == "darwin" {
		// /Applications/UmbraForge.app/Contents/MacOS/umbraforge
		cands = append(cands,
			filepath.Join(base, "..", "Resources", "plugins", "icloud-panel"),
			filepath.Join(base, "plugins", "icloud-panel"),
		)
	} else {
		cands = append(cands, filepath.Join(base, "plugins", "icloud-panel"))
	}
	names := []string{
		fmt.Sprintf("icloud-panel-%s-%s", runtime.GOOS, runtime.GOARCH),
		"icloud-panel",
	}
	if runtime.GOOS == "windows" {
		for i := range names {
			names[i] += ".exe"
		}
	}
	for _, dir := range cands {
		for _, n := range names {
			p := filepath.Join(dir, n)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

// StartPanel 启动内置平台服务：端口 8787 起、被占则递增；
// 健康就绪后自动注册+登录一个本地面板账号（首个注册=管理员），
// 后续 Apple 登录/创建/同步全部走该会话。
func (g *IcloudGateway) StartPanel(ctx context.Context, binPath string) (string, error) {
	g.mu.Lock()
	if g.panelCmd != nil {
		port := g.panelPort
		g.mu.Unlock()
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil // 已启动
	}
	binPath = strings.TrimSpace(binPath)
	if binPath == "" {
		binPath = probePanelBin()
	}
	if binPath == "" {
		g.mu.Unlock()
		return "", fmt.Errorf("未找到内置平台二进制（plugins/icloud-panel）")
	}
	if fi, err := os.Stat(binPath); err != nil || fi.IsDir() {
		g.mu.Unlock()
		return "", fmt.Errorf("内置平台二进制不可用: %s", binPath)
	}
	dataDir := filepath.Join(g.panelRunDir, "icloud-panel")
	if err := os.MkdirAll(filepath.Join(dataDir, "data"), 0o755); err != nil {
		g.mu.Unlock()
		return "", fmt.Errorf("创建平台数据目录失败: %w", err)
	}
	statePath := filepath.Join(dataDir, "data", "state.json")
	g.panelData = dataDir
	g.mu.Unlock()

	// 探测可用端口（8787 起，被占递增；用户自建平台占 8787 时自动避开）
	port := 8787
	// 先清理孤儿 panel 实例（本机同二进制，父进程退出后残留会共享
	// state.json 造成双实例竞争；面板面板进程无法自我识别归属，按
	// 二进制名清理，仅杀 icloud-panel 自己的进程）
	killOrphanPanels()
	for i := 0; i < 20; i++ {
		if !portInUse(port) {
			break
		}
		port++
	}
	cmd := procutil.Command(binPath, "--host", "127.0.0.1", "--port", fmt.Sprint(port),
		"--config", filepath.Join(dataDir, "config.json"), "--data", statePath)
	cmd.Dir = dataDir
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动内置平台失败: %w", err)
	}
	g.mu.Lock()
	g.panelCmd = cmd
	g.panelPort = port
	g.panelBin = binPath
	g.mu.Unlock()

	// 等健康
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
		resp, err := g.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	// 自动注册+登录本地面板账号（固定账号：GUI 与 headless 会话共享同一
	// 账号，Apple 登录态/邮箱数据互通；平台按用户隔离数据，随机账号会导致
	// 不同会话互相看不到对方登录的 Apple 账号）
	user := icloudPanelUser
	pass := icloudPanelPass
	if err := g.ensurePanelAccount(ctx, base, user, pass); err != nil {
		_ = g.StopPanel()
		return "", fmt.Errorf("平台自动账号初始化失败: %w", err)
	}
	g.mu.Lock()
	g.panelCreds = user + ":" + pass
	g.mu.Unlock()
	return base, nil
}

// ensurePanelAccount 注册（如不存在）并登录平台账号。
func (g *IcloudGateway) ensurePanelAccount(ctx context.Context, base, user, pass string) error {
	// 试登录（可能已注册）
	if err := g.Login(ctx, base, user, pass); err == nil {
		return nil
	}
	// 注册
	payload, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/auth/register", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Success bool `json:"success"`
		Error   *struct{ Message string `json:"message"` } `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Success {
		// 已存在但密码不同 → 用注册接口失败信息提示
		return fmt.Errorf("平台账号注册失败: %v", out.Error)
	}
	// 注册成功后登录
	return g.Login(ctx, base, user, pass)
}

// StopPanel 停止内置服务。
func (g *IcloudGateway) StopPanel() error {
	g.mu.Lock()
	cmd := g.panelCmd
	g.panelCmd = nil
	port := g.panelPort
	g.panelPort = 0
	g.mu.Unlock()
	if cmd == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	// 清理会话
	g.Logout(fmt.Sprintf("http://127.0.0.1:%d", port))
	return nil
}

func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// killOrphanPanels 清理残留的 icloud-panel 进程：
// 父进程（桌面版）异常退出后 panel 会成孤儿继续监听，导致下次启动
// 双实例共享 state.json。只杀二进制名含 icloud-panel 的进程
// （pgrep 自动排除自身）。
func killOrphanPanels() {
	out, err := procutil.Command("pgrep", "-f", "icloud-panel").Output()
	if err != nil {
		return // 无匹配
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid := strings.TrimSpace(line)
		if pid == "" {
			continue
		}
		_ = procutil.Command("kill", pid).Run()
	}
}

// ── Apple 官方登录（经内置平台转发）──

// AppleLoginStart 发起 Apple 登录（旧 iCloud 接口）。
// 返回 {pending_id, needs_2fa}；needs_2fa 时用户提交 6 位码。
func (g *IcloudGateway) AppleLoginStart(ctx context.Context, baseURL, appleID, password, method string) (map[string]any, error) {
	payload := map[string]string{
		"apple_id":  appleID,
		"password":  password,
		"two_factor_method": method,
	}
	raw, code, err := g.do(ctx, baseURL, http.MethodPost, "/api/icloud/protocol-login/start", payload)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized {
		return nil, fmt.Errorf("平台会话已失效，请重新启动内置服务")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Apple 登录响应解析失败: %w", err)
	}
	if ok, _ := out["success"].(bool); !ok {
		msg, _ := out["message"].(string)
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("Apple 登录失败: %s", msg)
	}
	return out, nil
}

// AppleLogin2FA 提交 Apple 2FA 验证码。
func (g *IcloudGateway) AppleLogin2FA(ctx context.Context, baseURL, pendingID, code string) (map[string]any, error) {
	payload := map[string]string{"pending_id": pendingID, "code": code}
	raw, code2, err := g.do(ctx, baseURL, http.MethodPost, "/api/icloud/protocol-login/2fa", payload)
	if err != nil {
		return nil, err
	}
	if code2 == http.StatusUnauthorized {
		return nil, fmt.Errorf("平台会话已失效，请重新启动内置服务")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("2FA 响应解析失败: %w", err)
	}
	if ok, _ := out["success"].(bool); !ok {
		msg, _ := out["message"].(string)
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("2FA 验证失败: %s", msg)
	}
	return out, nil
}

// DeleteMailbox 删除平台上的 HME 邮箱（含 Apple 侧）。
// 「用完即删」：HME 数量上限是并发存在数，删除后可再创建 ≈ 无限。
func (g *IcloudGateway) DeleteMailbox(ctx context.Context, baseURL, id string) error {
	raw, code, err := g.do(ctx, baseURL, http.MethodDelete, "/api/mailboxes/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return fmt.Errorf("平台会话已失效，请重新启动内置服务")
	}
	var out struct {
		Success bool `json:"success"`
		Error   *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("删除邮箱响应解析失败: %w", err)
	}
	if !out.Success {
		msg := "删除邮箱失败"
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return fmt.Errorf("%s", msg)
	}
	// 同步清掉本地池对应条目
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.loadPool()
	out2 := p.Entries[:0]
	for _, e := range p.Entries {
		if e.ID != id && e.Email != "" {
			out2 = append(out2, e)
		}
	}
	p.Entries = out2
	g.savePool(p)
	return nil
}

// SaveIMAPLogin 保存取码登录：主 iCloud 邮箱 + App 专用密码（IMAP 收信）。
// 平台设计：Apple 登录态只用于创建 HME；验证码收件走主邮箱 IMAP。
func (g *IcloudGateway) SaveIMAPLogin(ctx context.Context, baseURL, accountID, email, appPassword string) error {
	payload := map[string]string{
		"account_id":   accountID,
		"email":        email,
		"app_password": appPassword,
	}
	raw, code, err := g.do(ctx, baseURL, http.MethodPost, "/api/icloud/imap-login/save", payload)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return fmt.Errorf("平台会话已失效，请重新启动内置服务")
	}
	var out struct {
		Success bool `json:"success"`
		Error   *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("保存取码登录响应解析失败: %w", err)
	}
	if !out.Success {
		msg := "保存取码登录失败"
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// CheckIMAPLogin 检测已保存的取码登录（IMAP 收信）是否可用。
func (g *IcloudGateway) CheckIMAPLogin(ctx context.Context, baseURL, accountID string) (map[string]any, error) {
	payload := map[string]string{"account_id": accountID}
	raw, code, err := g.do(ctx, baseURL, http.MethodPost, "/api/icloud/imap-login/check", payload)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized {
		return nil, fmt.Errorf("平台会话已失效，请重新启动内置服务")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("检测响应解析失败: %w", err)
	}
	if ok, _ := out["success"].(bool); !ok {
		msg, _ := out["message"].(string)
		if msg == "" {
			msg = string(raw)
		}
		return nil, fmt.Errorf("收信检测失败: %s", msg)
	}
	return out, nil
}

// ── Apple 登录态直接调用（拿 iCloud 邮箱）──

// FetchForwardTarget 用 Apple 登录态调 HME 列表接口，返回转发目标
// （= 用户的 iCloud 邮箱地址，IMAP 登录名）。平台不暴露该字段，
// 这里照抄平台 ListPrivacyMailboxes 的调用方式（endpoint + headers + cookie）。
func (g *IcloudGateway) FetchForwardTarget(ctx context.Context) (string, error) {
	g.mu.Lock()
	statePath := filepath.Join(g.panelData, "data", "state.json")
	g.mu.Unlock()
	if statePath == "" || !fileExists(statePath) {
		return "", fmt.Errorf("未找到平台数据（请先启动内置服务并登录 Apple）")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return "", err
	}
	var st struct {
		ICloudSessions []struct {
			AppleID          string `json:"apple_id"`
			DSID             string `json:"dsid"`
			Host             string `json:"host"`
			ClientID         string `json:"client_id"`
			ClientBuild      string `json:"client_build_number"`
			MasteringNumber  string `json:"client_mastering_number"`
			PremiumMailBase  string `json:"premium_mail_base_url"`
			Cookies          []map[string]any `json:"cookies"`
		} `json:"icloud_sessions"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return "", err
	}
	if len(st.ICloudSessions) == 0 {
		return "", fmt.Errorf("无 Apple 登录态，请先在 iCloud 邮箱页登录 Apple")
	}
	s := st.ICloudSessions[0]
	if s.PremiumMailBase == "" || s.DSID == "" || len(s.Cookies) == 0 {
		return "", fmt.Errorf("Apple 登录态不完整（缺 mail 会话）")
	}
	// 组装 HME 列表请求（照抄平台 endpoint + setICloudFetchHeaders）
	base, err := url.Parse(strings.TrimRight(s.PremiumMailBase, "/") + "/")
	if err != nil {
		return "", err
	}
	rel := &url.URL{Path: "v2/hme/list"}
	u := base.ResolveReference(rel)
	q := u.Query()
	build := firstNonEmpty(s.ClientBuild, s.MasteringNumber, "2622Build20")
	q.Set("clientBuildNumber", build)
	q.Set("clientMasteringNumber", firstNonEmpty(s.MasteringNumber, s.ClientBuild, "2622Build20"))
	q.Set("clientId", firstNonEmpty(s.ClientID, "local-panel"))
	q.Set("dsid", s.DSID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	origin := "https://www.icloud.com"
	if strings.Contains(strings.ToLower(s.Host), "icloud.com.cn") {
		origin = "https://www.icloud.com.cn"
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36")
	if ck := appleCookiesHeader(s.Cookies, u.String()); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Apple HME 列表 HTTP %d: %s", resp.StatusCode, string(body[:minInt(len(body), 200)]))
	}
	var envelope struct {
		Success bool `json:"success"`
		Result  struct {
			HMEEmails []struct {
				HME            string `json:"hme"`
				ForwardToEmail string `json:"forwardToEmail"`
			} `json:"hmeEmails"`
		} `json:"result"`
		Error *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("HME 列表解析失败: %w", err)
	}
	if !envelope.Success {
		msg := "HME 列表获取失败"
		if envelope.Error != nil && envelope.Error.Message != "" {
			msg += ": " + envelope.Error.Message
		}
		return "", fmt.Errorf("%s", msg)
	}
	for _, item := range envelope.Result.HMEEmails {
		if fwd := strings.TrimSpace(item.ForwardToEmail); fwd != "" {
			return strings.ToLower(fwd), nil
		}
	}
	return "", fmt.Errorf("登录态有效但未取到转发目标（HME 列表为空？）")
}

// appleCookiesHeader 从登录态 cookie 列表组装 Cookie 头（仅保留 icloud 域）。
func appleCookiesHeader(cookies []map[string]any, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	var parts []string
	for _, c := range cookies {
		name, _ := c["name"].(string)
		val, _ := c["value"].(string)
		domain, _ := c["domain"].(string)
		if name == "" {
			continue
		}
		domain = strings.TrimPrefix(domain, ".")
		if domain != "" && !strings.HasSuffix(u.Hostname(), domain) && !strings.HasSuffix(domain, u.Hostname()) {
			continue
		}
		parts = append(parts, name+"="+val)
	}
	return strings.Join(parts, "; ")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DeleteMessagesByKeyword 删除收件箱中主题含关键字的邮件
// （验证码邮件用完即删，避免收件箱堆积）。
func (g *IcloudGateway) DeleteMessagesByKeyword(ctx context.Context, baseURL, accountID, keyword string) (int, error) {
	payload := map[string]string{"account_id": accountID, "keyword": keyword}
	raw, code, err := g.do(ctx, baseURL, http.MethodPost, "/api/icloud/messages/delete", payload)
	if err != nil {
		return 0, err
	}
	if code == http.StatusUnauthorized {
		return 0, fmt.Errorf("平台会话已失效，请重新登录")
	}
	var out struct {
		Success bool `json:"success"`
		Deleted int  `json:"deleted"`
		Error   *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("删除邮件响应解析失败: %w", err)
	}
	if !out.Success {
		msg := "删除邮件失败"
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return 0, fmt.Errorf("%s", msg)
	}
	return out.Deleted, nil
}

// ── 本地邮箱池 ──

func (g *IcloudGateway) loadPool() IcloudPool {
	var p IcloudPool
	raw, err := g.store.GetValue(context.Background(), icloudPoolKey)
	if err == nil && strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	if p.Entries == nil {
		p.Entries = []IcloudMailboxEntry{}
	}
	return p
}

func (g *IcloudGateway) savePool(p IcloudPool) {
	b, _ := json.Marshal(p)
	_ = g.store.Set(context.Background(), icloudPoolKey, string(b))
}

// SyncPool 把平台邮箱同步到本地池（新增 available，已存在保持状态）。
// 返回本次新增数。
func (g *IcloudGateway) SyncPool(ctx context.Context, baseURL string, accounts []IcloudAccount) (int, error) {
	mbs, err := g.Mailboxes(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	labels := map[string]string{}
	for _, a := range accounts {
		labels[a.ID] = a.Label
	}
	p := g.loadPool()
	seen := map[string]bool{}
	added := 0
	for _, m := range mbs {
		seen[m.Email] = true
		if m.Used {
			continue // 平台已标记 used 的不入池（注册会用到 available）
		}
		exists := false
		for i := range p.Entries {
			if p.Entries[i].Email == m.Email {
				exists = true
				if p.Entries[i].APIURL == "" {
					p.Entries[i].APIURL = m.APIURL
				}
				break
			}
		}
		if !exists {
			p.Entries = append(p.Entries, IcloudMailboxEntry{
				ID:           m.ID,
				Email:        m.Email,
				APIURL:       m.APIURL,
				AccountID:    m.AccountID,
				AccountLabel: labels[m.AccountID],
				Status:       "available",
			})
			added++
		}
	}
	g.savePool(p)
	return added, nil
}

// Pool 读本地池（可选账号过滤）。
func (g *IcloudGateway) Pool(accountIDs []string) IcloudPool {
	p := g.loadPool()
	if len(accountIDs) == 0 {
		return p
	}
	want := map[string]bool{}
	for _, id := range accountIDs {
		want[id] = true
	}
	var out []IcloudMailboxEntry
	for _, e := range p.Entries {
		if want[e.AccountID] {
			out = append(out, e)
		}
	}
	return IcloudPool{Entries: out}
}

// ClaimFromPool 从本地池随机领取一个可用邮箱（按账号过滤），标记 used。
// 返回条目；池空返回 (nil, nil)。随机而非顺序：顺序取会让同一批注册
// 反复用同一批邮箱，且每次注册拿到的邮箱不可预期（用户要求注册时随机）。
func (g *IcloudGateway) ClaimFromPool(accountIDs []string) *IcloudMailboxEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.loadPool()
	want := map[string]bool{}
	for _, id := range accountIDs {
		want[id] = true
	}
	var avail []int
	for i := range p.Entries {
		e := &p.Entries[i]
		if e.Status != "available" || strings.TrimSpace(e.Email) == "" {
			continue
		}
		if len(want) > 0 && !want[e.AccountID] {
			continue
		}
		avail = append(avail, i)
	}
	if len(avail) == 0 {
		return nil
	}
	idx := avail[rand.Intn(len(avail))]
	e := &p.Entries[idx]
	e.Status = "used"
	e.ClaimedAt = time.Now().Format(time.RFC3339)
	g.savePool(p)
	return e
}

// ReleasePoolEmail 释放一个邮箱回 available（注册失败重试/清理）。
func (g *IcloudGateway) ReleasePoolEmail(email string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.loadPool()
	for i := range p.Entries {
		if p.Entries[i].Email == email {
			p.Entries[i].Status = "available"
			p.Entries[i].ClaimedAt = ""
			break
		}
	}
	g.savePool(p)
}

// ResetPool 清空本地池（重新同步）。
func (g *IcloudGateway) ResetPool() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.savePool(IcloudPool{Entries: []IcloudMailboxEntry{}})
}

// URL 编码工具（导出用）。
func icloudPathEscape(s string) string { return url.PathEscape(s) }
