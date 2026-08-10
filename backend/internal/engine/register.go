package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	captchapkg "github.com/umbraforge/desktop/internal/pkg/captcha"
	"github.com/umbraforge/desktop/internal/pkg/grokregister"
	"github.com/umbraforge/desktop/internal/store"
)

const (
	KeyConfig  = "grok_register"
	KeyResults = "grok_register_results"
	KeyEdu     = "edu_email"
)

// Config is a desktop-local subset of GrokRegisterConfig.
type Config struct {
	CaptchaProvider    string `json:"captcha_provider"`
	EzSolverURL        string `json:"ezsolver_url"`
	EzSolverTimeoutSec int    `json:"ezsolver_timeout_sec"`
	EzSolverMaxConcur  int    `json:"ezsolver_max_concurrency"`
	EzSolverAPIKey     string `json:"ezsolver_api_key,omitempty"`
	// 打码失败自动重试次数（ezsolver_captcha_retry，默认 5，对齐 sub2api）
	EzCaptchaRetry     int    `json:"ezsolver_captcha_retry,omitempty"`
	AuralithURL        string `json:"auralith_url,omitempty"`
	AuralithAPIKey     string `json:"auralith_api_key,omitempty"`
	AuralithTimeoutSec int    `json:"auralith_timeout_sec,omitempty"`
	// iCloud Privacy Mail 取码平台接入（email_mode=icloud_api）：
	// 平台地址 + 全局 API Key + 项目标识（取码 keyword）+ 等待超时/轮询间隔。
	IcloudAPIBaseURL    string `json:"icloud_api_base_url,omitempty"`
	IcloudAPIKey        string `json:"icloud_api_key,omitempty"`
	IcloudAPIProject    string `json:"icloud_api_project,omitempty"`
	IcloudAPITimeoutSec int    `json:"icloud_api_timeout_sec,omitempty"`
	IcloudAPIInterval   int    `json:"icloud_api_interval_sec,omitempty"`
	AuralithMaxConcur   int    `json:"auralith_max_concurrency,omitempty"`
	AuralithRetry       int    `json:"auralith_retry,omitempty"`
	YesCaptchaKey       string `json:"yes_captcha_key,omitempty"`
	NextCaptchaKey      string `json:"next_captcha_key,omitempty"`
	DefaultCount        int    `json:"default_count"`
	DefaultConcurrency  int    `json:"default_concurrency"`
	DefaultEmailMode    string `json:"default_email_mode"`
	DefaultDelay        int    `json:"default_delay"`
	DefaultStepDelay    int    `json:"default_step_delay"`
	DefaultBrowser      string `json:"default_browser"`
	DefaultOSType       string `json:"default_os_type"`
	SkipVerify          bool   `json:"skip_verify"`
	AutoExport          bool   `json:"auto_export"`
	// 自动入库：注册成功自动 SSO→OAuth 转换并标记已入库（默认关，与 sub2api 一致）
	AutoImport bool `json:"auto_import"`
	// 入库并发（SSO→OAuth 转换线程数，默认 4）
	ImportConvertInflight int `json:"import_convert_inflight,omitempty"`
	// 入库代理模式：registration=跟随注册代理；direct=直连（默认跟随注册）
	ImportProxyMode string `json:"import_proxy_mode,omitempty"`
	// 入库到网关分组：注册成功自动入库时把账号落入该分组（空 = 不入组）。
	// GroupName 由前端随提交携带（engine 不依赖 gateway 包，避免循环依赖）。
	GatewayGroupID   string `json:"gateway_group_id,omitempty"`
	GatewayGroupName string `json:"gateway_group_name,omitempty"`
	// 注册出口代理选择：random=随机打乱（默认，与 sub2api 一致）；round_robin=顺序轮转
	ProxyPickMode    string `json:"proxy_pick_mode,omitempty"`
	CaptchaProxyMode string `json:"captcha_proxy_mode,omitempty"`
	// 连续失败自动暂停（上游风控避让）：连续失败 threshold 次后暂停 wait 分钟再继续，无限循环。
	AutoPauseOnFailures    bool `json:"auto_pause_on_failures"`
	AutoPauseWaitMinutes   int  `json:"auto_pause_wait_minutes,omitempty"`
	AutoPauseFailThreshold int  `json:"auto_pause_fail_threshold,omitempty"`
	// BatchConfig 批次调优（代理淘汰阈值 / 粘性复用间隔 / 每槽成功上限 /
	// 速率与节奏 / OTP 轮询 / 拟人延迟）。此前这一整组只有 DefaultBatchConfig()
	// 的硬编码值，既不落盘也不可配——注册页看到的
	// 「失败/取消，解除粘性以便重连换线」正是这组参数控制的行为。
	BatchConfig *grokregister.BatchConfig `json:"batch_config,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		CaptchaProvider:    "ezsolver",
		EzSolverURL:        "http://127.0.0.1:8192",
		EzSolverTimeoutSec: 120,
		EzSolverMaxConcur:  8, // 实测最优:打码闸8 > 注册并发5,给打码失败重试留余量（对齐 sub2api）
		EzCaptchaRetry:     5,
		AuralithURL:        "http://127.0.0.1:8194",
		AuralithTimeoutSec: 120,
		AuralithMaxConcur:  0, // 0=auto 跟随注册并发（对齐 sub2api：>0 才作显式闸）
		// iCloud 取码平台：默认 180s 等待 / 3s 轮询（对齐 web 版接入设计）
		IcloudAPITimeoutSec:    180,
		IcloudAPIInterval:      3,
		DefaultCount:           1,
		DefaultConcurrency:     1,
		DefaultEmailMode:       "mailtm",
		SkipVerify:             true,
		AutoPauseOnFailures:    true,
		AutoPauseWaitMinutes:   5,
		AutoPauseFailThreshold: 10,
		AutoImport:             false,
		ImportConvertInflight:  4,
		ImportProxyMode:        "registration",
		ProxyPickMode:          "random",
		DefaultDelay:           0,
		DefaultStepDelay:       0,
		DefaultBrowser:         "chrome",
		DefaultOSType:          "macos",
		CaptchaProxyMode:       "registration",
		BatchConfig:            grokregister.DefaultBatchConfig(),
	}
}

// normalizeBatchConfig 补齐批次调优的缺省值。
// 前端只传改动过的字段时，零值不能当成「用户要设 0」——除了少数确实允许 0 的项
// （min_proxy_interval / stagger_sec / warp_rotate_every / proxy_working_headroom），
// 其余一律回落到默认，避免一次部分保存把速率参数清零导致批次跑不动。
func normalizeBatchConfig(bc *grokregister.BatchConfig) *grokregister.BatchConfig {
	def := grokregister.DefaultBatchConfig()
	if bc == nil {
		return def
	}
	out := *bc
	if out.MaxProxyFails <= 0 {
		out.MaxProxyFails = def.MaxProxyFails
	}
	if out.MinProxyInterval < 0 {
		out.MinProxyInterval = 0
	}
	if out.MaxPerIP <= 0 {
		out.MaxPerIP = def.MaxPerIP
	}
	// 0 = 未设置（老配置文件没这个字段）→ 回落默认值；
	// 负数 = 用户在设置里显式关闭冷却 → 归一成 0（batch.go 用 >0 判断是否启用）。
	if out.ProxyCooldownMinutes == 0 {
		out.ProxyCooldownMinutes = def.ProxyCooldownMinutes
	} else if out.ProxyCooldownMinutes < 0 {
		out.ProxyCooldownMinutes = 0
	}
	if out.ProxyWorkingHeadroom < 0 {
		out.ProxyWorkingHeadroom = 0
	}
	// 流水线模式:nil(老配置没这个字段)= 默认开启;false = 用户显式关闭。
	if out.PipelineMode == nil {
		out.PipelineMode = def.PipelineMode
	}
	if out.RateCapacity <= 0 {
		out.RateCapacity = def.RateCapacity
	}
	if out.RateBase <= 0 {
		out.RateBase = def.RateBase
	}
	if out.RateMin <= 0 {
		out.RateMin = def.RateMin
	}
	if out.StaggerSec < 0 {
		out.StaggerSec = 0
	}
	if strings.TrimSpace(out.WarpRotateMode) == "" {
		out.WarpRotateMode = def.WarpRotateMode
	}
	if out.WarpRotateEvery < 0 {
		out.WarpRotateEvery = 0
	}
	if out.OTPPollMs <= 0 {
		out.OTPPollMs = def.OTPPollMs
	}
	return &out
}

type StartInput struct {
	Count            int      `json:"count"`
	Concurrency      int      `json:"concurrency"`
	Delay            int      `json:"delay"`
	StepDelay        int      `json:"step_delay"`
	EmailMode        string   `json:"email_mode"`
	OutlookData      string   `json:"outlook_data"`
	CaptchaProvider  string   `json:"captcha_provider"`
	Browser          string   `json:"browser"`
	OSType           string   `json:"os_type"`
	SkipVerify       bool     `json:"skip_verify"`
	Proxy            string   `json:"proxy"`
	ProxyURLs        []string `json:"proxy_urls"`
	CaptchaProxyMode string   `json:"captcha_proxy_mode"`
	CFWorkerIDs      []string `json:"cf_worker_ids"`
	IcloudAccountIDs []string `json:"icloud_account_ids,omitempty"` // icloud_api 模式按账号选邮箱（空=全部）
	// 显式关池：空 proxy_urls 会被服务端回填代理池，
	// 只有这个标记为 false 才是真直连。指针区分「未传」与「传了 false」。
	UseProxyPool *bool `json:"use_proxy_pool,omitempty"`
	// 任务级网关分组：注册成功入库时落入该分组（空 = 跟随配置/不入组）。
	GatewayGroupID   string `json:"gateway_group_id,omitempty"`
	GatewayGroupName string `json:"gateway_group_name,omitempty"`
}

// PoolOptOut 表示调用方显式要求直连（关闭代理池），
// 此时服务端不得用代理池回填空的 ProxyURLs。
func (in StartInput) PoolOptOut() bool {
	return in.UseProxyPool != nil && !*in.UseProxyPool
}

type PerMinStat struct {
	Minute  int64 `json:"minute"` // unix 分钟（秒级时间戳 / 60）
	Success int   `json:"success"`
	Fail    int   `json:"fail"`
}

type Status struct {
	Running    bool             `json:"running"`
	Stopping   bool             `json:"stopping"`
	Total      int              `json:"total"`
	Completed  int              `json:"completed"`
	Success    int              `json:"success"`
	Fail       int              `json:"fail"`
	Message    string           `json:"message"`
	LastEmail  string           `json:"last_email"`
	LastError  string           `json:"last_error"`
	ElapsedSec int              `json:"elapsed_sec"`
	Engine     string           `json:"engine"`
	Captcha    string           `json:"captcha_provider"`
	Results    []map[string]any `json:"results,omitempty"`
	// PerMin 每分钟成功/失败数（最近 30 分钟，分钟粒度，按时间升序）。
	// 采样线程每 30s 检查一次分钟边界，跨分钟时把该分钟的增量记录进来；
	// 失败不算成功，用户按「每分钟成功多少个」考核吞吐。
	PerMin []PerMinStat `json:"per_min,omitempty"`
	// CaptchaGate / CaptchaLive 打码并发闸与实际 worker 数（本批启动时采样）。
	// 闸 = max(设置值, 注册并发×2)（打码池余量，对齐 sub2api 的 2 倍设计）；
	// live 是打码器实际 worker 数。瞬时活跃打码数远小于闸是正常的——
	// 注册 worker 大部分时间在邮箱/提交，只有一小段在等打码。
	CaptchaGate int `json:"captcha_gate,omitempty"`
	CaptchaLive int `json:"captcha_live,omitempty"`
	// 连续失败自动暂停激活状态 + 剩余秒数（前端倒计时）
	AutoPaused            bool `json:"auto_paused"`
	AutoPauseRemainingSec int  `json:"auto_pause_remaining_sec"`

	// ── 自动入库（SSO→OAuth）实时统计 ──
	//
	// 注册页此前完全看不到入库情况：自动入库在 onResult 里内联调用
	// ConvertSSOToOAuth，既不进 importProgressTracker（那个只服务「账号管理页
	// 手动入库」），也不在 status 里留任何痕迹。用户开着「自动入库」跑一批，
	// 只能从日志里一行行找 [自动入库]，数不出成功几个、失败几个、花了多久。
	AutoImportEnabled bool `json:"auto_import_enabled"`
	// ImportDone 已出结果的入库数（成功+失败），ImportSuccess/ImportFailed 分项。
	ImportDone    int `json:"import_done"`
	ImportSuccess int `json:"import_success"`
	ImportFailed  int `json:"import_failed"`
	// ImportElapsedMS 累计入库耗时（各账号转换耗时之和，非墙钟）。
	// 用累计而非墙钟：转换是穿插在注册里做的，墙钟等于整批耗时，说明不了入库成本。
	ImportElapsedMS int64 `json:"import_elapsed_ms"`
	// ImportLastEmail / ImportLastError 最近一次入库的账号与失败原因。
	ImportLastEmail string `json:"import_last_email"`
	ImportLastError string `json:"import_last_error"`
	// PendingImportCount 账号库里「有 SSO 但没 OAuth 凭证」的待入库数量。
	// 自动入库失败的账号会留在这里等重试，注册页要能一眼看到积压。
	PendingImportCount int `json:"pending_import_count"`
}

type RegisterEngine struct {
	store    *store.JSONStore
	accounts *AccountService
	icloud   *IcloudGateway

	mu       sync.Mutex
	cancel   *grokregister.SafeCancel
	running  bool
	stopping bool
	status   Status
	logs     []string
	results  []map[string]any
	started  time.Time

	// perMin 采样状态：perMinLast 是上一 tick 的分钟号（0=未初始化），
	// perMinSucc/perMinFail 是上一 tick 的累计成功/失败数，用于算分钟增量。
	perMin     []PerMinStat
	perMinLast int64
	perMinSucc int
	perMinFail int

	// workerSink 接收打码并发上限（由 plugins.Center 实现）；见 captcha_workers.go
	workerSink CaptchaWorkerSink
}

func NewRegisterEngine(st *store.JSONStore, accounts *AccountService) *RegisterEngine {
	e := &RegisterEngine{store: st, accounts: accounts, status: Status{Engine: "local"}}
	e.icloud = NewIcloudGateway(st)
	e.status.Results = nil
	grokregister.SetLogSink(func(line string) { e.appendLog(line) })
	return e
}

// Icloud 返回 iCloud 取码平台网关（登录/账号/邮箱/本地池）。
func (e *RegisterEngine) Icloud() *IcloudGateway { return e.icloud }

// deleteIcloudVerifiedMail 验证码通过验证后删除收件箱里该验证码邮件
// （IMAP 按主题关键字删，避免收件箱堆积）。
func (e *RegisterEngine) deleteIcloudVerifiedMail(email string) {
	if e.icloud == nil {
		return
	}
	base := e.icloud.PanelURL()
	if base == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ent := e.icloud.PoolEntryByEmail(email)
	accountID := ""
	if ent != nil {
		accountID = ent.AccountID
	}
	n, err := e.icloud.DeleteMessagesByKeyword(ctx, base, accountID, "SpaceXAI")
	if err != nil {
		e.appendLog(fmt.Sprintf("iCloud: 验证码邮件删除失败: %v", err))
		return
	}
	if n > 0 {
		e.appendLog(fmt.Sprintf("iCloud: 已删除验证码邮件 %d 封（收件箱不堆积）", n))
	}
}

// deleteIcloudMailboxAfterSuccess 注册成功后自动删除用过的 HME：
// 数量上限是并发存在数，删除即可再建（用户「用完即删」要求）。
func (e *RegisterEngine) deleteIcloudMailboxAfterSuccess(email string) {
	ent := e.icloud.PoolEntryByEmail(email)
	if ent == nil || ent.ID == "" {
		return
	}
	base := e.icloud.PanelURL()
	if base == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.icloud.DeleteMailbox(ctx, base, ent.ID); err == nil {
		e.appendLog(fmt.Sprintf("iCloud: 注册成功，已自动删除 HME %s（配额释放）", email))
	} else {
		e.appendLog(fmt.Sprintf("iCloud: HME 自动删除失败 %s: %v", email, err))
	}
}

// recordAutoImport 累计一次自动入库结果，供注册页实时显示进度/数量/耗时。
// 在 onResult 回调里调用，而 onResult 由多个 worker 并发触发，所以必须持锁。
func (e *RegisterEngine) recordAutoImport(email string, ok bool, errMsg string, elapsedMS int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.ImportDone++
	e.status.ImportElapsedMS += elapsedMS
	e.status.ImportLastEmail = email
	if ok {
		e.status.ImportSuccess++
		e.status.ImportLastError = ""
		return
	}
	e.status.ImportFailed++
	e.status.ImportLastError = truncStr(errMsg, 200)
}

func (e *RegisterEngine) appendLog(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	ts := time.Now().Format("15:04:05")
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, ts+" "+line)
	if len(e.logs) > 2000 {
		e.logs = e.logs[len(e.logs)-2000:]
	}
}

func (e *RegisterEngine) GetLogs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.logs))
	copy(out, e.logs)
	return out
}

func (e *RegisterEngine) ClearLogs() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = nil
}

func (e *RegisterEngine) GetConfig(ctx context.Context) (Config, error) {
	raw, err := e.store.GetValue(ctx, KeyConfig)
	if err != nil {
		return DefaultConfig(), err
	}
	cfg := DefaultConfig()
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg, nil
}

func (e *RegisterEngine) SaveConfig(ctx context.Context, cfg Config) (Config, error) {
	cur, _ := e.GetConfig(ctx)
	// merge zeros/empties from partial updates onto current defaults
	if cfg.CaptchaProvider == "" {
		cfg.CaptchaProvider = cur.CaptchaProvider
	}
	if cfg.EzSolverURL == "" {
		cfg.EzSolverURL = cur.EzSolverURL
	}
	if cfg.EzSolverTimeoutSec <= 0 {
		cfg.EzSolverTimeoutSec = cur.EzSolverTimeoutSec
	}
	// 0=auto 跟随注册并发是合法值（与 auralith_max_concurrency 一致）；
	// 只有负数才视为未设置、回退旧值
	if cfg.EzSolverMaxConcur < 0 {
		cfg.EzSolverMaxConcur = cur.EzSolverMaxConcur
	}
	if cfg.AuralithURL == "" {
		cfg.AuralithURL = cur.AuralithURL
	}
	if cfg.DefaultCount <= 0 {
		cfg.DefaultCount = cur.DefaultCount
	}
	if cfg.DefaultConcurrency <= 0 {
		cfg.DefaultConcurrency = cur.DefaultConcurrency
	}
	if cfg.DefaultEmailMode == "" {
		cfg.DefaultEmailMode = cur.DefaultEmailMode
	}
	if cfg.DefaultBrowser == "" {
		cfg.DefaultBrowser = cur.DefaultBrowser
	}
	if cfg.DefaultOSType == "" {
		cfg.DefaultOSType = cur.DefaultOSType
	}
	if cfg.CaptchaProxyMode == "" {
		cfg.CaptchaProxyMode = cur.CaptchaProxyMode
	}
	// paid captcha secrets: empty / *** keep previous
	if cfg.YesCaptchaKey == "" || cfg.YesCaptchaKey == "***" {
		cfg.YesCaptchaKey = cur.YesCaptchaKey
	}
	if cfg.NextCaptchaKey == "" || cfg.NextCaptchaKey == "***" {
		cfg.NextCaptchaKey = cur.NextCaptchaKey
	}
	if cfg.EzSolverAPIKey == "" || cfg.EzSolverAPIKey == "***" {
		cfg.EzSolverAPIKey = cur.EzSolverAPIKey
	}
	if cfg.AuralithAPIKey == "" || cfg.AuralithAPIKey == "***" {
		cfg.AuralithAPIKey = cur.AuralithAPIKey
	}
	// iCloud 取码平台：key 空/*** 保留旧值；地址空保留；超时/间隔 <=0 保留默认
	if cfg.IcloudAPIKey == "" || cfg.IcloudAPIKey == "***" {
		cfg.IcloudAPIKey = cur.IcloudAPIKey
	}
	if cfg.IcloudAPIBaseURL == "" {
		cfg.IcloudAPIBaseURL = cur.IcloudAPIBaseURL
	}
	if cfg.IcloudAPITimeoutSec <= 0 {
		cfg.IcloudAPITimeoutSec = cur.IcloudAPITimeoutSec
	}
	if cfg.IcloudAPIInterval <= 0 {
		cfg.IcloudAPIInterval = cur.IcloudAPIInterval
	}
	if cfg.AuralithTimeoutSec <= 0 {
		cfg.AuralithTimeoutSec = cur.AuralithTimeoutSec
	}
	// 0=auto 跟随注册并发是合法值；只有负数才视为未设置回退旧值
	if cfg.AuralithMaxConcur < 0 {
		cfg.AuralithMaxConcur = cur.AuralithMaxConcur
	}
	if cfg.EzCaptchaRetry <= 0 {
		cfg.EzCaptchaRetry = cur.EzCaptchaRetry
	}
	if cfg.DefaultCount <= 0 {
		cfg.DefaultCount = 1
	}
	if cfg.DefaultConcurrency <= 0 {
		cfg.DefaultConcurrency = 1
	}
	if cfg.EzSolverURL == "" {
		cfg.EzSolverURL = "http://127.0.0.1:8192"
	}
	if cfg.EzSolverTimeoutSec <= 0 {
		cfg.EzSolverTimeoutSec = 120
	}
	if cfg.EzSolverMaxConcur < 0 {
		cfg.EzSolverMaxConcur = 0
	}
	if cfg.EzCaptchaRetry <= 0 {
		cfg.EzCaptchaRetry = 5
	}
	if cfg.DefaultEmailMode == "" {
		cfg.DefaultEmailMode = "mailtm"
	}
	if cfg.DefaultBrowser == "" {
		cfg.DefaultBrowser = "chrome"
	}
	if cfg.DefaultOSType == "" {
		cfg.DefaultOSType = runtimeGOOS()
	}
	// 批次调优：未传时保留现有（而不是被 nil 清掉），传了则补齐缺省
	if cfg.BatchConfig == nil {
		cfg.BatchConfig = cur.BatchConfig
	}
	cfg.BatchConfig = normalizeBatchConfig(cfg.BatchConfig)
	b, _ := json.Marshal(cfg)
	if err := e.store.Set(ctx, KeyConfig, string(b)); err != nil {
		return cfg, err
	}
	// 打码并发改了要立刻下发给插件中心，否则要等下次重启应用才生效
	// （旧实现只在 main.go 启动时推一次）。
	e.pushCaptchaWorkers(cfg, 0)
	return cfg, nil
}

func runtimeGOOS() string {
	// small indirection to avoid importing runtime at top if already imported
	return currentGOOS()
}

func (e *RegisterEngine) Status(_ context.Context) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.status
	if e.running && !e.started.IsZero() {
		st.ElapsedSec = int(time.Since(e.started).Seconds())
	}
	st.Results = append([]map[string]any(nil), e.results...)
	st.PerMin = append([]PerMinStat(nil), e.perMin...)
	return st
}

func (e *RegisterEngine) Start(ctx context.Context, in StartInput) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("already running")
	}
	cfg, err := e.GetConfig(ctx)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if in.Count <= 0 {
		in.Count = cfg.DefaultCount
	}
	if in.Concurrency <= 0 {
		in.Concurrency = cfg.DefaultConcurrency
	}
	if in.EmailMode == "" {
		in.EmailMode = cfg.DefaultEmailMode
	}
	if in.CaptchaProvider == "" {
		in.CaptchaProvider = cfg.CaptchaProvider
	}
	if in.Browser == "" {
		in.Browser = cfg.DefaultBrowser
	}
	if in.OSType == "" {
		in.OSType = cfg.DefaultOSType
	}
	// 任务级网关分组覆盖（注册时选择的分组优先于配置；空 = 跟随配置）
	if strings.TrimSpace(in.GatewayGroupID) != "" {
		cfg.GatewayGroupID = strings.TrimSpace(in.GatewayGroupID)
		cfg.GatewayGroupName = strings.TrimSpace(in.GatewayGroupName)
	}
	// Pre-validate email modes so UI gets a sync error instead of a silent empty batch.
	mode := strings.ToLower(strings.TrimSpace(in.EmailMode))
	if mode == "edu" || mode == "cfworker" {
		workers := e.loadEduWorkers(ctx)
		idSet := map[string]bool{}
		for _, id := range in.CFWorkerIDs {
			id = strings.TrimSpace(id)
			if id != "" {
				idSet[id] = true
			}
		}
		n := 0
		for _, w := range workers {
			if !w.Enabled {
				continue
			}
			if len(idSet) > 0 && !idSet[w.ID] {
				continue
			}
			if strings.TrimSpace(w.Domain) != "" && strings.TrimSpace(w.WorkerURL) != "" {
				n++
			}
		}
		if n == 0 {
			e.mu.Unlock()
			return fmt.Errorf("EDU 邮箱模式需要至少一个已启用 Worker：请到「EDU 邮箱」开通/入池")
		}
	}
	if mode == "outlook" {
		if len(grokregister.ParseOutlookData(in.OutlookData, "")) == 0 {
			e.mu.Unlock()
			return fmt.Errorf("Outlook 模式需要 outlook_data（email----password----client_id----refresh_token）")
		}
	}
	cancel := grokregister.NewSafeCancel()
	e.cancel = cancel
	e.running = true
	e.stopping = false
	e.started = time.Now()
	e.results = nil
	e.resetPerMin()
	e.status = Status{
		Running: true,
		Total:   in.Count,
		Engine:  "local",
		Captcha: in.CaptchaProvider,
		Message: "starting",
		// 整个 Status 被重建，入库计数自然归零 —— 这正是每批重新计数的语义。
		// AutoImportEnabled 要在这里定下来：前端据此决定显示不显示入库面板，
		// 而 cfg 在 run() 里才用得到，那时前端可能已经轮询过好几拍了。
		AutoImportEnabled: cfg.AutoImport,
	}
	e.mu.Unlock()

	go e.run(ctx, cfg, in, cancel)
	return nil
}

func (e *RegisterEngine) run(parent context.Context, cfg Config, in StartInput, cancel *grokregister.SafeCancel) {
	defer func() {
		e.mu.Lock()
		e.running = false
		e.stopping = false
		e.status.Running = false
		e.status.Message = "idle"
		// 定格本轮总耗时：Status() 只在 running 时算 elapsed，
		// 不落盘的话收尾瞬间前端的速率/ETA 会归零。
		if !e.started.IsZero() {
			e.status.ElapsedSec = int(time.Since(e.started).Seconds())
		}
		// 收尾前把最后一个未满分钟也记录进去（用户考核「每分钟成功数」，
		// 最后一段经常跑不满 60s，不 flush 会少算一整分钟）。
		e.flushPerMinLocked(time.Now().Unix()/60, e.status.Success, e.status.Fail)
		e.mu.Unlock()
		e.persistResults()
	}()

	// 每分钟成功/失败采样（30s tick 检查分钟边界；cancel 时退出）
	go e.samplePerMin(cancel)

	proxyPool := append([]string{}, in.ProxyURLs...)
	if p := strings.TrimSpace(in.Proxy); p != "" {
		proxyPool = append(proxyPool, p)
	}
	// 代理选择模式：random=每次 start 随机打乱（避免固定从列表最前开始）；round_robin=保持顺序
	if cfg.ProxyPickMode == "random" && len(proxyPool) > 1 {
		rand.Shuffle(len(proxyPool), func(i, j int) { proxyPool[i], proxyPool[j] = proxyPool[j], proxyPool[i] })
	}

	captchaURL := cfg.EzSolverURL
	captchaKey := cfg.EzSolverAPIKey
	switch strings.ToLower(in.CaptchaProvider) {
	case "auralith":
		captchaURL = cfg.AuralithURL
		captchaKey = cfg.AuralithAPIKey
	case "yescaptcha":
		captchaKey = cfg.YesCaptchaKey
	case "nextcaptcha":
		captchaKey = cfg.NextCaptchaKey
	case "veloraturn":
		if captchaURL == "" || strings.Contains(captchaURL, "8192") {
			captchaURL = "http://127.0.0.1:8193"
		}
	}

	// ── 打码运行时注入（对齐 sub2api grok_register_service.go:2777-2852）──
	// 此前从未调用 SetCaptchaRuntime：SolveViaProxy 恒 false（打码从不走注册代理）、
	// EzSolver gate 恒 1（ezsolver_max_concurrency 配置无效）、CaptchaRetry 恒默认。
	// 本段补齐后：注册/打码/入库全程同一粘性代理 + 打码并发真正生效。
	captchaProv := strings.ToLower(strings.TrimSpace(in.CaptchaProvider))
	captchaMode := firstNonEmpty(in.CaptchaProxyMode, cfg.CaptchaProxyMode, "registration")
	capMode := grokregister.NormalizeCaptchaProxyMode(captchaMode, true)
	useProxyForSolve := grokregister.CaptchaProxyModeUsesProxy(capMode)

	// 打码并发跟随注册并发：设置值当上限，注册并发当实际驱动量（详见
	// captcha_workers.go）。旧实现直接用设置值，选并发 1 也会拉起 12 个浏览器。
	maxConcur := resolveCaptchaWorkers(cfg.EzSolverMaxConcur, in.Concurrency)
	if maxConcur < 1 {
		maxConcur = 1
	}
	// 下发给插件中心：打码进程重启（本机/Docker）时按这个值注入 MAX_WORKERS
	e.pushCaptchaWorkers(cfg, in.Concurrency)
	// 开跑前自愈打码器：Docker 容器被引擎重启杀掉后会停在 Exited，
	// 不先拉起来的话这一整批都会因为解题连不上而失败。
	e.ensureCaptchaReady(captchaProv)
	ezTimeoutMS := cfg.EzSolverTimeoutSec * 1000
	if ezTimeoutMS <= 0 {
		ezTimeoutMS = 120000
	}
	ezCfg := grokregister.EzSolverConfig{
		URL:            captchaURL,
		APIKey:         cfg.EzSolverAPIKey,
		TimeoutMS:      ezTimeoutMS,
		MaxConcurrency: maxConcur, // 严格跟随设置，不写死
		Enabled:        true,
	}
	auTimeoutMS := cfg.AuralithTimeoutSec * 1000
	if auTimeoutMS <= 0 {
		auTimeoutMS = 120000
	}
	// Auralith 并发决策（对齐 sub2api decideAuralithConcurrency，1:1）：
	// 设置值>0 → 闸=设置值；0/auto → 保留 live workers，仅在 live < 注册并发
	// 时扩容到注册并发（不主动降 live、不做 ×2 余量——12 核机器拉起 20 个
	// Chrome 纯属浪费）。
	auGate := resolveCaptchaWorkers(cfg.AuralithMaxConcur, in.Concurrency)
	if captchaProv == "auralith" || captchaProv == "au" {
		if ac, aerr := captchapkg.NewAuralithClientValidated(captchaURL, max(1, auTimeoutMS/1000), cfg.AuralithAPIKey); aerr == nil {
			snap := ac.Health(context.Background())
			live := snap.Workers
			if live <= 0 {
				live = ac.Config(context.Background()).Workers
			}
			decision := decideAuralithConcurrency(in.Concurrency, cfg.AuralithMaxConcur, live)
			auGate = decision.Gate
			// 透出给前端：注册并发 → 闸 → 实际 workers 的对应关系，
			// 用户能直接看到「闸=N live=N」而不是从瞬时活跃数猜。
			e.mu.Lock()
			e.status.CaptchaGate = auGate
			e.status.CaptchaLive = live
			e.mu.Unlock()
			e.appendLog(fmt.Sprintf("[Captcha] Auralith 并发: 注册=%d 闸=%d live=%d settings=%d(0=auto:1:1跟随注册,保留live)",
				in.Concurrency, auGate, live, cfg.AuralithMaxConcur))
			if decision.SetWorkers {
				if wsnap := ac.SetWorkers(context.Background(), decision.Workers); wsnap.OK || wsnap.Workers > 0 {
					e.appendLog(fmt.Sprintf("[Captcha] Auralith workers 已从 %d 调整为 %d", live, wsnap.Workers))
				}
			}
		}
	}
	if auGate < 1 {
		auGate = in.Concurrency
	}
	if auGate < 1 {
		auGate = 1
	}
	grokregister.SetCaptchaRuntime(grokregister.CaptchaRuntime{
		DefaultProvider: grokregister.CaptchaProvider(captchaProv),
		EzSolver:        ezCfg,
		Auralith: grokregister.AuralithConfig{
			URL:            strings.TrimSpace(cfg.AuralithURL),
			APIKey:         cfg.AuralithAPIKey,
			TimeoutMS:      auTimeoutMS,
			MaxConcurrency: auGate,
			Enabled:        true,
		},
		YesCaptchaKey:  cfg.YesCaptchaKey,
		NextCaptchaKey: cfg.NextCaptchaKey,
		YesEnabled:     true,
		SolveViaProxy:  useProxyForSolve,
		CaptchaRetry:   cfg.EzCaptchaRetry,
		AuralithRetry:  cfg.AuralithRetry,
	})
	if useProxyForSolve {
		e.appendLog(fmt.Sprintf("[Captcha] 打码代理模式=%s：跳过 token 预热（注册/打码/入库全程同一代理，按 worker 实时解题）", captchaMode))
	}

	// 把打码并发推到各 free solver host（对齐 sub2api SyncEzSolverWorkers）。
	// 注意：这一步与「是否走代理解题」无关——旧实现把它和 token 预热绑在同一个
	// `!useProxyForSolve` 分支里，而默认打码代理模式是 registration（走代理），
	// 条件恒假 → workers 从来没被推下去 → EzSolver 一直是缺省 12 个浏览器。
	if captchaProv == "ezsolver" || captchaProv == "veloraturn" || captchaProv == "auto" || captchaProv == "" {
		go func(n int, raw, key string, sec int) {
			for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
				return r == ',' || r == ';' || r == '|' || r == '\n'
			}) {
				u := strings.TrimSpace(part)
				if u == "" {
					continue
				}
				if c := captchapkg.NewEzSolverClientWithKey(u, max(1, sec), key); c != nil {
					if wsnap := c.SetWorkers(context.Background(), n); wsnap.OK || wsnap.Workers > 0 {
						e.appendLog(fmt.Sprintf("[Captcha] %s workers 已同步=%d（注册并发=%d 设置上限=%d）",
							u, wsnap.Workers, in.Concurrency, cfg.EzSolverMaxConcur))
					}
				}
			}
		}(maxConcur, captchaURL, cfg.EzSolverAPIKey, ezTimeoutMS/1000)
	}

	// 免费 token 预热。走注册代理时必须关预热：预热 token 用服务器 IP 解题，
	// 代理模式下 worker 不会领用。
	if (captchaProv == "ezsolver" || captchaProv == "veloraturn" || captchaProv == "auto" || captchaProv == "") && !useProxyForSolve {
		warmTarget := maxConcur * 3
		if warmTarget > in.Count {
			warmTarget = in.Count
		}
		if warmTarget > 24 {
			warmTarget = 24
		}
		warmProvider := grokregister.CaptchaProviderEzSolver
		warmLabel := "EzSolver Python"
		if captchaProv == "veloraturn" {
			warmProvider = grokregister.CaptchaProviderVeloraTurn
			warmLabel = "VeloraTurn Go"
		}
		grokregister.ConfigureEzTokenWarmup(ezCfg, grokregister.CaptchaOptions{
			Provider: warmProvider, EzSolverURL: captchaURL,
		}, "https://accounts.x.ai/sign-up", "0x4AAAAAAAhr9JGVDZbrZOo0", warmTarget)
		e.appendLog(fmt.Sprintf("[Captcha] %s 并发=%d url=%s timeout=%dms — 正在预热 token 库 target=%d；达到门槛前不启动代理注册", warmLabel, maxConcur, captchaURL, ezTimeoutMS, warmTarget))
	}

	captchaOpts := grokregister.CaptchaOptions{
		Provider:       grokregister.CaptchaProvider(in.CaptchaProvider),
		EzSolverURL:    captchaURL,
		EzSolverAPIKey: captchaKey,
		YesCaptchaKey:  cfg.YesCaptchaKey,
		NextCaptchaKey: cfg.NextCaptchaKey,
		AuralithURL:    cfg.AuralithURL,
		AuralithAPIKey: cfg.AuralithAPIKey,
	}
	switch strings.ToLower(in.CaptchaProvider) {
	case "auralith":
		captchaOpts.AuralithURL = captchaURL
		captchaOpts.AuralithAPIKey = captchaKey
	case "veloraturn", "ezsolver":
		captchaOpts.EzSolverURL = captchaURL
		captchaOpts.EzSolverAPIKey = captchaKey
	}

	emailMode := in.EmailMode
	if emailMode == "edu" {
		emailMode = "cfworker"
	}
	e.appendLog(fmt.Sprintf("收到注册请求: 邮箱模式=%s count=%d 并发=%d 打码=%s", emailMode, in.Count, in.Concurrency, in.CaptchaProvider))

	var emailSvcs []grokregister.EmailService
	// EDU/CF workers loaded from edu store (optional id filter)
	if emailMode == "cfworker" {
		workers := e.loadEduWorkers(parent)
		idSet := map[string]bool{}
		for _, id := range in.CFWorkerIDs {
			id = strings.TrimSpace(id)
			if id != "" {
				idSet[id] = true
			}
		}
		for _, w := range workers {
			if !w.Enabled {
				continue
			}
			if len(idSet) > 0 && !idSet[w.ID] {
				continue
			}
			if strings.TrimSpace(w.Domain) == "" || strings.TrimSpace(w.WorkerURL) == "" {
				continue
			}
			svc := grokregister.NewCFWorkerEmail(w.Domain, w.WorkerURL, w.ApiKey, w.CFAccountID, w.CFApiToken, w.KVNamespaceID)
			if svc != nil {
				svc.SetUseEduSubdomain(w.UseEduSubdomain)
				emailSvcs = append(emailSvcs, svc)
			}
		}
		if len(emailSvcs) == 0 {
			e.appendLog("EDU 模式失败: 无可用 Worker（请在 EDU 页开通/入池并启用）")
			e.mu.Lock()
			e.status.Message = "EDU 无可用 Worker"
			e.status.LastError = "EDU 邮箱模式需要至少一个已启用 Worker"
			e.status.Fail = in.Count
			e.status.Completed = in.Count
			e.mu.Unlock()
			return
		}
		e.appendLog(fmt.Sprintf("EDU 邮箱池: %d 个 Worker", len(emailSvcs)))
	}
	if emailMode == "outlook" {
		proxy0 := ""
		if len(proxyPool) > 0 {
			proxy0 = proxyPool[0]
		}
		emailSvcs = grokregister.ParseOutlookData(in.OutlookData, proxy0)
		if len(emailSvcs) == 0 {
			e.appendLog("Outlook 模式失败: outlook_data 为空或格式错误")
			e.mu.Lock()
			e.status.Message = "Outlook 数据无效"
			e.status.LastError = "需要 outlook_data（email----password----client_id----refresh_token）"
			e.status.Fail = in.Count
			e.status.Completed = in.Count
			e.mu.Unlock()
			return
		}
		e.appendLog(fmt.Sprintf("Outlook 账号: %d 个", len(emailSvcs)))
	}
	// iCloud Privacy Mail 取码平台（icloud_api）：每个 worker 一个取号请求。
	// Create 时 POST /api/v1/mailboxes/claim 自动取号，WaitForCode 轮询取码。
	if emailMode == "icloud_api" || emailMode == "icloud" {
		// base_url 必填；api_key 仅 claim 回退需要（池模式/自动创建
		// 用内置服务会话，无需 Key）——所以 key 缺失只警告不阻断。
		// 配置没地址但内置服务在跑 → 自动用内置地址（登录即用，零配置）。
		baseURL := strings.TrimSpace(cfg.IcloudAPIBaseURL)
		if baseURL == "" && e.icloud.PanelPort() > 0 {
			baseURL = fmt.Sprintf("http://127.0.0.1:%d", e.icloud.PanelPort())
			e.appendLog(fmt.Sprintf("iCloud 模式: 使用内置平台 %s", baseURL))
		}
		// 还没启动内置服务但平台数据存在（Apple 登录态/邮箱池）→
		// 自动启动内置平台并等待就绪，注册零配置直跑
		if baseURL == "" && e.icloud.HasPanelData() {
			e.appendLog("iCloud 模式: 自动启动内置平台服务…")
			if _, err := e.icloud.StartPanel(context.Background(), ""); err != nil {
				e.appendLog(fmt.Sprintf("iCloud 模式失败: 内置平台自动启动失败: %v", err))
				e.mu.Lock()
				e.status.Message = "iCloud 平台启动失败"
				e.status.LastError = err.Error()
				e.status.Fail = in.Count
				e.status.Completed = in.Count
				e.mu.Unlock()
				return
			}
			if p := e.icloud.PanelPort(); p > 0 {
				baseURL = fmt.Sprintf("http://127.0.0.1:%d", p)
				e.appendLog(fmt.Sprintf("iCloud 模式: 内置平台已启动 %s", baseURL))
			}
		}
		if baseURL == "" {
			e.appendLog("iCloud 模式失败: 未配置平台地址（iCloud 邮箱页启动内置服务或设置地址）")
			e.mu.Lock()
			e.status.Message = "iCloud 配置缺失"
			e.status.LastError = "需要 icloud_api_base_url（iCloud 取码平台）"
			e.status.Fail = in.Count
			e.status.Completed = in.Count
			e.mu.Unlock()
			return
		}
		timeoutSec := cfg.IcloudAPITimeoutSec
		if timeoutSec <= 0 {
			timeoutSec = 180
		}
		interval := time.Duration(cfg.IcloudAPIInterval)
		if interval <= 0 {
			interval = 3
		}
		for i := 0; i < in.Concurrency; i++ {
			// 池模式：从本地池领取（按账号过滤），池空回退平台 claim
			svc := NewICloudPoolEmail(e.icloud, baseURL, cfg.IcloudAPIKey, cfg.IcloudAPIProject, in.IcloudAccountIDs)
			svc.SetCancel(cancel)
			svc.SetLogPrefix("iCloud")
			emailSvcs = append(emailSvcs, svc)
		}
		sel := "全部账号"
		if len(in.IcloudAccountIDs) > 0 {
			sel = fmt.Sprintf("%d 个账号", len(in.IcloudAccountIDs))
		}
		e.appendLog(fmt.Sprintf("iCloud 取码平台: %d 个取号槽（%s · %s · 超时 %ds · 轮询 %ds）",
			in.Concurrency, baseURL, sel, timeoutSec, interval))
	}

	opts := grokregister.BatchOptions{
		Count:            in.Count,
		Concurrency:      in.Concurrency,
		DelaySec:         in.Delay,
		StepDelay:        in.StepDelay,
		SkipVerify:       in.SkipVerify || cfg.SkipVerify,
		Browser:          in.Browser,
		OSType:           in.OSType,
		EmailMode:        emailMode,
		EmailServices:    emailSvcs,
		ProxyPool:        proxyPool,
		CaptchaProxyMode: firstNonEmpty(in.CaptchaProxyMode, cfg.CaptchaProxyMode, "registration"),
		Captcha:          captchaOpts,
		// 此前从不设置，batch.go 只能回落 DefaultBatchConfig()，
		// 于是代理淘汰阈值/粘性间隔/每槽上限等全是硬编码，UI 改不动。
		BatchConfig: normalizeBatchConfig(cfg.BatchConfig),
		// 连续失败自动暂停（上游风控避让）
		AutoPauseOnFailures:    cfg.AutoPauseOnFailures,
		AutoPauseWaitMinutes:   cfg.AutoPauseWaitMinutes,
		AutoPauseFailThreshold: cfg.AutoPauseFailThreshold,
		OnProgress: func(p grokregister.BatchProgress) {
			e.mu.Lock()
			e.status.Total = p.Total
			e.status.Completed = p.Completed
			e.status.Success = p.Success
			e.status.Fail = p.Fail
			e.status.Message = p.Message
			e.status.LastEmail = p.LastEmail
			e.status.AutoPaused = p.AutoPaused
			e.status.AutoPauseRemainingSec = p.AutoPauseRemainingSec
			e.status.LastError = p.LastError
			e.status.Running = p.Running
			e.mu.Unlock()
		},
		// 验证码通过验证后立即删除收件箱里的验证码邮件
		// （用完即删：收件箱不堆积；HME 子邮箱留到入库成功后删，
		// 提交失败重试可复用同一邮箱重新取码）
		OnOTPVerified: func(email string) {
			if emailMode == "icloud_api" || emailMode == "icloud" {
				go e.deleteIcloudVerifiedMail(email)
			}
		},
		OnResult: func(r grokregister.RegisterResult) {
			// keep full credentials for vault; UI status may mask later
			full := map[string]any{}
			for k, v := range r {
				full[k] = v
			}
			e.mu.Lock()
			e.results = append(e.results, sanitizeResult(full))
			if len(e.results) > 500 {
				e.results = e.results[len(e.results)-500:]
			}
			e.mu.Unlock()
			st, _ := full["status"].(string)
			// iCloud 模式：失败/取消 → 释放邮箱回池（可复用，
			// 不浪费 HME 配额；成功则由 deleteIcloudMailboxAfterSuccess 删除）
			if !strings.EqualFold(st, "success") && (emailMode == "icloud_api" || emailMode == "icloud") {
				if email, _ := full["email"].(string); email != "" {
					go e.icloud.ReleasePoolEmail(email)
				}
			}
			// iCloud 模式：注册成功即自动删除该 HME（用完即删，
			// 释放 Apple 配额，删除后可再创建 ≈ 无限）
			if strings.EqualFold(st, "success") && (emailMode == "icloud_api" || emailMode == "icloud") {
				if email, _ := full["email"].(string); email != "" {
					go e.deleteIcloudMailboxAfterSuccess(email)
				}
			}
			if strings.EqualFold(st, "success") && e.accounts != nil {
				// 自动入库(SSO→OAuth 转换 + 账号保存)异步执行:
				// 转换走代理访问 x.ai,同步执行会阻塞 worker(实测 8-9s/个),
				// 3 并发时注册吞吐从 ~12/分钟被拖到 ~8/分钟。
				// 后台完成后照常写日志/统计/入库,不影响注册流水线。
				go func() {
					email, _ := full["email"].(string)
					sso := strFrom(full, "sso")
					_, hasTok := full["access_token"].(string)
					// 自动入库：无 OAuth 凭证时补做 SSO→OAuth 转换（与 sub2api 入库一致）
					if cfg.AutoImport && !hasTok && sso != "" {
						ssoRw := strFrom(full, "sso_rw", "sso-rw")
						if ssoRw == "" {
							ssoRw = sso
						}
						proxy, _ := full["proxy"].(string)
						useProxy := strings.TrimSpace(proxy) != "" && cfg.ImportProxyMode != "direct"
						if useProxy && !strings.Contains(proxy, "://") {
							useProxy = false
						}
						convStart := time.Now()
						// 入库重试：默认 3 次（2s/4s 退避）——OAuth 转换失败多为
						// 瞬时网络抖动，一次失败直接放弃太浪费（SSO 保留兜底）。
						var tok *grokregister.GrokOAuthToken
						var cerr error
						for attempt := 0; attempt < 3; attempt++ {
							tok, cerr = grokregister.ConvertSSOToOAuth(context.Background(), sso, ssoRw, proxy, useProxy)
							if cerr == nil && tok != nil {
								break
							}
							if attempt < 2 {
								time.Sleep(time.Duration(2*(attempt+1)) * time.Second)
							}
						}
						convMS := time.Since(convStart).Milliseconds()
						if cerr == nil && tok != nil {
							full["access_token"] = tok.AccessToken
							full["refresh_token"] = tok.RefreshToken
							full["id_token"] = tok.IDToken
							full["token_type"] = tok.TokenType
							full["expires_in"] = tok.ExpiresIn
							full["imported"] = true
							e.recordAutoImport(email, true, "", convMS)
							e.appendLog(fmt.Sprintf("[自动入库] %s 转换成功 → 已入库（%.1fs）", email, float64(convMS)/1000))
						} else {
							full["imported"] = false
							e.recordAutoImport(email, false, fmt.Sprint(cerr), convMS)
							e.appendLog(fmt.Sprintf("[自动入库] %s 转换失败（保留 SSO，可稍后重试）: %v", email, cerr))
						}
					} else if hasTok {
						full["imported"] = true
					}
					// 网关分组落组：配置了目标分组时随账号入库写入（空 = 不入组）
					if gid := strings.TrimSpace(cfg.GatewayGroupID); gid != "" {
						full["group_id"] = gid
						full["group_name"] = strings.TrimSpace(cfg.GatewayGroupName)
					}
					if _, err := e.accounts.UpsertFromResult(context.Background(), full); err != nil {
						e.appendLog("账号保存失败: " + err.Error())
					} else {
						e.appendLog("账号已保存: " + email)
					}
				}()
			}
		},
	}

	e.appendLog(fmt.Sprintf("Grok-Nobody batch start id=%s count=%d concurrency=%d captcha=%s email=%s", uuid.NewString()[:8], in.Count, in.Concurrency, in.CaptchaProvider, emailMode))
	prog := grokregister.RunBatch(opts, cancel)
	grokregister.StopEzTokenWarmup("批量注册结束：停止预热并释放槽位")
	e.mu.Lock()
	e.status.Total = prog.Total
	e.status.Completed = prog.Completed
	e.status.Success = prog.Success
	e.status.Fail = prog.Fail
	e.status.Message = prog.Message
	e.status.LastEmail = prog.LastEmail
	e.status.LastError = prog.LastError
	e.status.Running = false
	e.mu.Unlock()
	e.appendLog(fmt.Sprintf("batch done success=%d fail=%d", prog.Success, prog.Fail))
}

type eduWorker struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Domain          string `json:"domain"`
	WorkerURL       string `json:"worker_url"`
	ApiKey          string `json:"api_key"`
	CFAccountID     string `json:"cf_account_id"`
	CFApiToken      string `json:"cf_api_token"`
	KVNamespaceID   string `json:"kv_namespace_id"`
	Enabled         bool   `json:"enabled"`
	UseEduSubdomain bool   `json:"use_edu_subdomain"`
}

type eduStore struct {
	Workers []eduWorker `json:"workers"`
}

func (e *RegisterEngine) loadEduWorkers(ctx context.Context) []eduWorker {
	raw, err := e.store.GetValue(ctx, KeyEdu)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var st eduStore
	if json.Unmarshal([]byte(raw), &st) != nil {
		return nil
	}
	return st.Workers
}

func (e *RegisterEngine) Stop(_ context.Context) error {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return nil
	}
	e.stopping = true
	e.status.Stopping = true
	e.status.Message = "stopping"
	cancel := e.cancel
	e.mu.Unlock()
	// 锁外做清理：StopEzTokenWarmup 内部持 ezPoolMu，若预热填充在跑，
	// 在锁内调用会把 engine 锁一并拖死（status/stop 全部无响应，表现为
	// 「点停止后整个应用卡死」）。先放锁，再停预热、关 cancel。
	grokregister.StopEzTokenWarmup("停止注册：释放 EzSolver 预热与浏览器")
	if cancel != nil {
		cancel.Close()
	}
	return nil
}

// samplePerMin 每 30s 检查一次分钟边界，跨分钟时记录该分钟的
// 成功/失败增量到 e.perMin（环形保留最近 30 分钟）。
func (e *RegisterEngine) samplePerMin(cancel *grokregister.SafeCancel) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancel.Done():
			return
		case <-ticker.C:
		}
		e.mu.Lock()
		e.flushPerMinLocked(time.Now().Unix()/60, e.status.Success, e.status.Fail)
		e.mu.Unlock()
	}
}

// flushPerMinLocked 把当前累计值结算进 perMin（调用方必须持 e.mu）。
// 分钟号没变就只更新基准（不产生新条目）；变了就把上一分钟的增量入列。
func (e *RegisterEngine) flushPerMinLocked(minute int64, succ, fail int) {
	if e.perMinLast == 0 {
		e.perMinLast = minute
		e.perMinSucc, e.perMinFail = succ, fail
		return
	}
	if minute == e.perMinLast {
		// 同一分钟：只滚动基准（30s tick 同一分钟可能来两次，
		// 不滚动的话跨分钟增量会把整分钟的累计值全算进去）。
		e.perMinSucc, e.perMinFail = succ, fail
		return
	}
	e.perMin = append(e.perMin, PerMinStat{
		Minute:  minute,
		Success: succ - e.perMinSucc,
		Fail:    fail - e.perMinFail,
	})
	if len(e.perMin) > 30 {
		e.perMin = e.perMin[len(e.perMin)-30:]
	}
	e.perMinLast = minute
	e.perMinSucc, e.perMinFail = succ, fail
}

// run 结束或 Start 重置时调用（持锁）。
func (e *RegisterEngine) resetPerMin() {
	e.perMin = nil
	e.perMinLast = 0
	e.perMinSucc, e.perMinFail = 0, 0
	e.status.PerMin = nil
}

func (e *RegisterEngine) persistResults() {
	e.mu.Lock()
	b, _ := json.Marshal(e.results)
	e.mu.Unlock()
	_ = e.store.Set(context.Background(), KeyResults, string(b))
}

func currentGOOS() string { return runtime.GOOS }

func sanitizeResult(full map[string]any) map[string]any {
	entry := map[string]any{}
	for _, k := range []string{"status", "email", "error", "browser", "os", "proxy", "registration_proxy", "at", "created_at", "password", "sso", "sso_token", "sso_rw"} {
		if v, ok := full[k]; ok {
			entry[k] = v
		}
	}
	// mask secrets in status feed
	for _, k := range []string{"password", "sso", "sso_token", "sso_rw", "access_token", "refresh_token", "id_token"} {
		if v, ok := entry[k].(string); ok && v != "" {
			entry[k] = maskSecret(v)
		}
	}
	if _, ok := entry["at"]; !ok {
		entry["at"] = time.Now().Format(time.RFC3339)
	}
	return entry
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-3:]
}
