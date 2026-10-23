package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 本文件是 Codex 打票（官方 Codex x-codex-turn-state 票据）的核心服务。
//
// 票据语义（与 codex-rs 对齐）：上游在「一个回合」的首个响应头里铸造
// x-codex-turn-state（Fernet 形态，gAAAAA 前缀，默认 292 字节；team /
// selfservebusinessprolite 账号 332 字节），客户端在同一回合的后续请求中原样回带。
// 服务端的职责：
//  1. 用账号自身 OAuth 凭据主动向上游打一发探测请求，从响应头捕获票据 +
//     Cloudflare 基础设施 Cookie，按 账号×模型 存到 accounts.extra；
//  2. 转发时若客户端没有回带 turn-state，则注入已捕获票据（客户端自带的回带
//     永远不覆盖——那是本回合的真实血统，覆盖会打断回合链）；
//  3. 观测上游响应：拿到新票时捕获；发现注入票据被上游换掉且 __oailb 变化时记
//     失效流水并清掉该代票据，交给后台重打；
//  4. 失败/限流有节奏控制（指数区间 + 抖动），成功票据按周期主动刷新；
//  5. 打票走代理池轮换（复用本地「IP 设置」里的代理），全局默认关闭。
//
// 本文件只新增文件，不修改任何既有文件；main.go / forward 路径的接线由 Lead 完成。

const (
	// codexTurnTicketExtraKeyPrefix 是 accounts.extra 中票据键的前缀，键为
	// "codex_turn_ticket:<model>"。与参考实现（sub4api）保持一致，方便存量数据迁移。
	codexTurnTicketExtraKeyPrefix = "codex_turn_ticket:"
	// CodexTicketStatePrefix 是官方 Codex 票据的固定前缀（Fernet token 的 base64 头）。
	CodexTicketStatePrefix = "gAAAAA"

	// 账号级打票参与开关（accounts.extra）：
	//   codex_ticket_harvest_enabled=false 表示该账号整体退出打票；
	//   codex_ticket_harvest_models={model:false} 表示单模型退出。
	codexTicketAccountEnabledKey = "codex_ticket_harvest_enabled"
	codexTicketModelsEnabledKey  = "codex_ticket_harvest_models"
	// codexTicketAllowWithoutTicketKey 是账号级「无票放行」覆盖（优先于全局）。
	codexTicketAllowWithoutTicketKey = "codex_allow_without_ticket"

	// codexTicketSettingsTTL 是进程内设置快照的 TTL：热路径不读 DB。
	codexTicketSettingsTTL = 10 * time.Second

	// codexTicketExpiredReuseWindow 是票据过期后的兜底复用窗口：
	// 上游对已过期票据通常仍接受同回合回带；窗口内优先复用避免打票抖动。
	codexTicketExpiredReuseWindow = 600 * time.Second

	// codexTicketHistoryCleanupInterval 是流水清理周期（保留窗口在 repository 层）。
	codexTicketHistoryCleanupInterval = time.Hour

	// codexTicketHarvestConcurrency 限制单轮扫描的并发探测数。
	codexTicketHarvestConcurrency = 4
)

// codexTicket 是持久化在 accounts.extra 里的票据当前值。
// 注意：State / Cookie 属于账号级机密，绝不进入流水表或管理端列表接口。
type codexTicket struct {
	AccountID          int64     `json:"account_id"`
	Model              string    `json:"model"`
	GenerationID       string    `json:"generation_id"`
	VerificationMethod string    `json:"verification_method,omitempty"`
	State              string    `json:"state"`
	Cookie             string    `json:"cookie,omitempty"`
	Length             int       `json:"length"`
	CapturedAt         time.Time `json:"captured_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	HTTPStatus         int       `json:"http_status,omitempty"`
}

// structurallyValid 只校验票据形状，不校验有效期：
// 长度必须命中账号规则（team/selfservebusinessprolite 332，其余 292），前缀必须是 gAAAAA。
func (t *codexTicket) structurallyValid(account *Account, configuredLength int) bool {
	if t == nil {
		return false
	}
	return codexTicketStateValid(account, configuredLength, t.State) && strings.TrimSpace(t.GenerationID) != ""
}

// usable 判断票据能否用于注入。过期票据在兜底窗口内仍可复用，由 reusingExpired 标识。
func (t *codexTicket) usable(now time.Time) (usable bool, reusingExpired bool) {
	if t == nil || strings.TrimSpace(t.State) == "" {
		return false, false
	}
	if t.ExpiresAt.IsZero() || now.Before(t.ExpiresAt) {
		return true, false
	}
	if now.Before(t.ExpiresAt.Add(codexTicketExpiredReuseWindow)) {
		return true, true
	}
	return false, false
}

// codexTicketRuntimeSettings 是进程内设置快照：全局开关 / 无票放行 / 节奏 / 代理池。
type codexTicketRuntimeSettings struct {
	cfg                config.CodexTicketConfig
	enabled            bool
	allowWithoutTicket bool
	cadence            CodexTicketCadence
	pool               CodexTicketProxyPool
	loadedAt           time.Time
}

// codexTicketInjectionSnapshot 记录「本请求注入了哪一代票据」，供 Observe 判断
// 上游是否换掉了我们注入的票据。只保留最近一次注入，命中后即删除。
type codexTicketInjectionSnapshot struct {
	generationID string
	state        string
	cookie       string
	expiresAt    time.Time
}

// CodexTicketDeps 是 CodexTicketService 的构造依赖。
// 前四个字段是硬依赖（main.go 均已持有）；其余为可选增强，为空时服务自动降级。
type CodexTicketDeps struct {
	AccountRepo AccountRepository
	SettingSvc  *SettingService
	ProxyRepo   ProxyRepository
	Cfg         *config.Config

	// Upstream 是网关共用的上游 HTTP 执行器（含代理/TLS 指纹能力）。
	// 为空时服务使用内置 http.Client（仅用于测试/降级，不启用代理池）。
	Upstream HTTPUpstream
	// TokenProvider 提供可刷新的 OAuth access token；为空时直接读账号凭据。
	TokenProvider CodexTicketTokenProvider
	// 以下仓储由 repository 包新增实现；为空时相关功能自动降级（不影响打票主链路）。
	Attempts  CodexTicketAttemptRepository
	Lifecycle CodexTicketLifecycleRepository
	Events    CodexTicketEventRepository
	Recent    CodexTicketRecentRepository
}

// CodexTicketService 是 Codex 打票与票据管理的核心服务。
// 并发安全：Start/Stop 串行化；票据/探测状态用 sync.Map；设置快照用 atomic.Value。
type CodexTicketService struct {
	accountRepo   AccountRepository
	settingSvc    *SettingService
	proxyRepo     ProxyRepository
	cfg           *config.Config
	upstream      HTTPUpstream
	tokenProvider CodexTicketTokenProvider
	attempts      CodexTicketAttemptRepository
	lifecycle     CodexTicketLifecycleRepository
	events        CodexTicketEventRepository
	recent        CodexTicketRecentRepository
	extraStore    CodexTicketExtraStore
	fallbackHTTP  *http.Client
	// probeURL 是打票探测的上游地址，默认 chatgptCodexURL；单测可指向 httptest。
	probeURL string

	baseSettings atomic.Value // *codexTicketRuntimeSettings

	settingsMu sync.Mutex
	settings   atomic.Value // *codexTicketRuntimeSettings

	tickets    sync.Map // ticketKey -> *codexTicket
	inflight   sync.Map // ticketKey -> struct{}
	nextRetry  sync.Map // ticketKey -> time.Time
	proxyTurns sync.Map // ticketKey -> *atomic.Uint64
	injected   sync.Map // ticketKey -> codexTicketInjectionSnapshot

	startMu sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	started bool
	stopped bool
}

// ticketKey 是进程内缓存键：(accountID, model)。
func codexTicketCacheKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

// codexTicketExtraKey 是 accounts.extra 中票据键。
func codexTicketExtraKey(model string) string {
	return codexTurnTicketExtraKeyPrefix + strings.TrimSpace(model)
}

// IsCodexTicketExtraKey 判断 extra 键是否由打票服务管理（账号编辑/导出时必须保留/脱敏）。
func IsCodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), codexTurnTicketExtraKeyPrefix)
}

// MergeCodexTicketExtra 在管理员编辑账号时保留服务端管理的票据键，避免旧快照覆盖新票据。
// 返回值是新 map，不修改入参。
func MergeCodexTicketExtra(extra, current map[string]any) map[string]any {
	result := make(map[string]any, len(extra)+2)
	for key, value := range extra {
		if IsCodexTicketExtraKey(key) {
			continue
		}
		result[key] = value
	}
	for key, value := range current {
		if IsCodexTicketExtraKey(key) {
			result[key] = value
		}
	}
	return result
}

// RedactCodexTicketExtra 在账号导出/备份时剥离票据本体。
func RedactCodexTicketExtra(extra map[string]any) map[string]any {
	redacted := make(map[string]any, len(extra))
	for key, value := range extra {
		if IsCodexTicketExtraKey(key) {
			continue
		}
		redacted[key] = value
	}
	return redacted
}

// NewCodexTicketService 构造 Codex 打票服务。构造不发起任何网络/DB 请求。
func NewCodexTicketService(deps CodexTicketDeps) *CodexTicketService {
	cfg := config.CodexTicketConfig{}
	if deps.Cfg != nil {
		cfg = deps.Cfg.CodexTicket()
	} else {
		cfg = (&config.Config{}).CodexTicket()
	}
	cfg = config.NormalizeCodexTicketConfig(cfg)
	service := &CodexTicketService{
		accountRepo:   deps.AccountRepo,
		settingSvc:    deps.SettingSvc,
		proxyRepo:     deps.ProxyRepo,
		cfg:           deps.Cfg,
		upstream:      deps.Upstream,
		tokenProvider: deps.TokenProvider,
		attempts:      deps.Attempts,
		lifecycle:     deps.Lifecycle,
		events:        deps.Events,
		recent:        deps.Recent,
		fallbackHTTP: &http.Client{
			// 探测请求可能在上游排队；超时由单次 attempt 的 context 控制。
			Timeout: 0,
		},
		probeURL: chatgptCodexURL,
	}
	// 账号仓储若实现了 extra 窄接口，就绕过 UpdateExtra 的调度 outbox 副作用。
	if store, ok := deps.AccountRepo.(CodexTicketExtraStore); ok {
		service.extraStore = store
	}
	base := &codexTicketRuntimeSettings{
		cfg:                cfg,
		enabled:            cfg.Enabled,
		allowWithoutTicket: !cfg.FailClosed,
		cadence: CodexTicketCadence{
			RetryMinSeconds: cfg.HarvestRetryMinSeconds,
			RetryMaxSeconds: cfg.HarvestRetryMaxSeconds,
			RefreshSeconds:  cfg.HarvestRefreshSeconds,
		},
		pool:     CodexTicketProxyPool{Mode: CodexTicketPoolModeAll, ProxyIDs: []int64{}},
		loadedAt: time.Now(),
	}
	service.baseSettings.Store(base)
	service.settings.Store(base)
	return service
}

// isCodexTicketAccount 判断账号是否参与打票：OpenAI OAuth / SetupToken，且非影子账号。
// 影子账号不持有自己的凭据，也不应持有自己的票据。
func isCodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// CodexTicketParticipationEnabled 读取账号级参与开关（默认参与）。
func CodexTicketParticipationEnabled(account *Account, model string) bool {
	if account == nil {
		return false
	}
	if enabled, ok := account.Extra[codexTicketAccountEnabledKey].(bool); ok && !enabled {
		return false
	}
	model = strings.TrimSpace(model)
	switch models := account.Extra[codexTicketModelsEnabledKey].(type) {
	case map[string]any:
		if participating, ok := models[model]; ok {
			if value, ok := participating.(bool); ok {
				return value
			}
		}
		return true
	case map[string]bool:
		if participating, ok := models[model]; ok {
			return participating
		}
		return true
	default:
		return true
	}
}

// CodexTicketAllowsWithoutTicket 判断「无票是否允许裸打业务请求」：
// 账号级覆盖优先于全局默认；未参与打票的账号/模型一律放行。
func CodexTicketAllowsWithoutTicket(account *Account, model string, globalDefault bool) bool {
	if account != nil {
		if !CodexTicketParticipationEnabled(account, model) {
			return true
		}
		if allow, ok := account.Extra[codexTicketAllowWithoutTicketKey].(bool); ok {
			return allow
		}
	}
	return globalDefault
}

// codexTicketTargetLength 按账号规则给出目标票据长度：
// team / selfservebusinessprolite 账号 332 字节，其余使用配置值（默认 292）。
func codexTicketTargetLength(account *Account, configured int) int {
	if account != nil && account.IsOpenAIOAuthLike() {
		plan := strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\n', '_', '-':
				return -1
			}
			return r
		}, strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type"))))
		if plan == "team" || plan == "selfservebusinessprolite" {
			return config.OpenAICodexTicketTeamTargetLength
		}
	}
	if configured > 0 {
		return configured
	}
	return config.DefaultOpenAICodexTicketTargetLength
}

// codexTicketStateValid 校验票据形状：前缀 + 账号规则长度。
func codexTicketStateValid(account *Account, configuredLength int, state string) bool {
	state = strings.TrimSpace(state)
	if !strings.HasPrefix(state, CodexTicketStatePrefix) {
		return false
	}
	return len(state) == codexTicketTargetLength(account, configuredLength)
}

// normalizeCodexTicketModel 归一化模型名（去除首尾空白；模型名大小写敏感，不做小写化）。
func normalizeCodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

// CodexTicketReadyModels 返回账号 extra 中已有结构合法票据的模型集合。
// 管理端账号列表/调度侧可用它做轻量投影，不需要解析票据内容。
func CodexTicketReadyModels(account *Account) map[string]bool {
	ready := make(map[string]bool)
	if account == nil {
		return ready
	}
	for key, raw := range account.Extra {
		if !IsCodexTicketExtraKey(key) {
			continue
		}
		model := strings.TrimPrefix(key, codexTurnTicketExtraKeyPrefix)
		if model == "" {
			continue
		}
		if ticket := parseCodexTicket(account, model, raw); ticket != nil {
			ready[model] = true
		}
	}
	return ready
}

// parseCodexTicket 从 accounts.extra 的原始值解析票据；形状不合法时返回 nil。
func parseCodexTicket(account *Account, model string, raw any) *codexTicket {
	if raw == nil {
		return nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket codexTicket
	if err := json.Unmarshal(payload, &ticket); err != nil {
		return nil
	}
	if account != nil {
		ticket.AccountID = account.ID
	}
	if model = normalizeCodexTicketModel(model); model != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	ticket.Cookie = strings.TrimSpace(ticket.Cookie)
	if ticket.Length <= 0 {
		ticket.Length = len(ticket.State)
	}
	if !ticket.structurallyValid(account, 0) {
		return nil
	}
	return &ticket
}

// lookupTicket 读取账号当前票据：account.Extra 是事实来源，进程内缓存只做兜底。
func (s *CodexTicketService) lookupTicket(account *Account, model string) *codexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeCodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := codexTicketCacheKey(account.ID, model)
	if account.Extra != nil {
		if raw, ok := account.Extra[codexTicketExtraKey(model)]; ok {
			if ticket := parseCodexTicket(account, model, raw); ticket != nil {
				s.tickets.Store(key, ticket)
				return ticket
			}
			s.tickets.Delete(key)
			return nil
		}
	}
	if raw, ok := s.tickets.Load(key); ok {
		if ticket, ok := raw.(*codexTicket); ok {
			return ticket
		}
	}
	return nil
}

// storeTicket 持久化票据到 accounts.extra 并更新进程内缓存/账号快照。
func (s *CodexTicketService) storeTicket(ctx context.Context, account *Account, ticket *codexTicket) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return ErrCodexTicketUnavailable
	}
	ticket.AccountID = account.ID
	ticket.Model = normalizeCodexTicketModel(ticket.Model)
	if !ticket.structurallyValid(account, s.baseConfig().TargetLength) {
		return fmt.Errorf("%w: invalid ticket shape", ErrCodexTicketUnavailable)
	}
	key := codexTicketExtraKey(ticket.Model)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	if s.extraStore != nil {
		err = s.extraStore.UpdateCodexTicketExtra(writeCtx, account.ID, key, ticket)
	} else if s.accountRepo != nil {
		err = s.accountRepo.UpdateExtra(writeCtx, account.ID, map[string]any{key: ticket})
	} else {
		return ErrCodexTicketUnavailable
	}
	if err != nil {
		return err
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[key] = ticket
	s.tickets.Store(codexTicketCacheKey(account.ID, ticket.Model), ticket)
	return nil
}

// removeCachedTicket 在失效后清掉进程内缓存（DB 中的 extra 键由生命周期仓储原子删除）。
func (s *CodexTicketService) removeCachedTicket(accountID int64, model string) {
	s.tickets.Delete(codexTicketCacheKey(accountID, model))
	s.injected.Delete(codexTicketCacheKey(accountID, model))
}

// baseConfig 返回构造时归一化的静态配置。
func (s *CodexTicketService) baseConfig() config.CodexTicketConfig {
	if s == nil {
		return config.NormalizeCodexTicketConfig((&config.Config{}).CodexTicket())
	}
	if base, ok := s.baseSettings.Load().(*codexTicketRuntimeSettings); ok && base != nil {
		return base.cfg
	}
	return config.NormalizeCodexTicketConfig((&config.Config{}).CodexTicket())
}

// cachedSettings 返回当前设置快照，绝不触发 DB 读取（供 BlocksAccount 等无 ctx 路径）。
func (s *CodexTicketService) cachedSettings() *codexTicketRuntimeSettings {
	if s == nil {
		return &codexTicketRuntimeSettings{}
	}
	if cached, ok := s.settings.Load().(*codexTicketRuntimeSettings); ok && cached != nil {
		return cached
	}
	return &codexTicketRuntimeSettings{}
}

// runtimeSettings 返回设置快照：TTL 内直接命中；过期时返回旧值并异步刷新，
// 保证转发热路径永远不会因设置读取而阻塞。
func (s *CodexTicketService) runtimeSettings(ctx context.Context) *codexTicketRuntimeSettings {
	cached := s.cachedSettings()
	if !cached.loadedAt.IsZero() && time.Since(cached.loadedAt) < codexTicketSettingsTTL {
		return cached
	}
	if cached.loadedAt.IsZero() {
		return s.loadSettings(ctx)
	}
	go s.refreshSettingsAsync()
	return cached
}

func (s *CodexTicketService) refreshSettingsAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.loadSettings(ctx)
}

// loadSettings 从 SettingService 读取开关/放行/节奏/代理池，失败时回退静态配置。
func (s *CodexTicketService) loadSettings(ctx context.Context) *codexTicketRuntimeSettings {
	if s == nil {
		return &codexTicketRuntimeSettings{}
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	// 并发合并：等待锁期间别的 goroutine 可能已经刷新过。
	if cached, ok := s.settings.Load().(*codexTicketRuntimeSettings); ok && cached != nil &&
		!cached.loadedAt.IsZero() && time.Since(cached.loadedAt) < time.Second {
		return cached
	}
	base := s.baseConfig()
	next := &codexTicketRuntimeSettings{
		cfg:                base,
		enabled:            base.Enabled,
		allowWithoutTicket: !base.FailClosed,
		cadence: CodexTicketCadence{
			RetryMinSeconds: base.HarvestRetryMinSeconds,
			RetryMaxSeconds: base.HarvestRetryMaxSeconds,
			RefreshSeconds:  base.HarvestRefreshSeconds,
		},
		pool:     CodexTicketProxyPool{Mode: CodexTicketPoolModeAll, ProxyIDs: []int64{}},
		loadedAt: time.Now(),
	}
	if s.settingSvc != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		loadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		next.enabled = s.settingSvc.GetCodexTicketEnabled(loadCtx, base.Enabled)
		next.allowWithoutTicket = s.settingSvc.GetCodexTicketAllowWithoutTicket(loadCtx, !base.FailClosed)
		next.cadence = s.settingSvc.GetCodexTicketCadence(loadCtx, next.cadence)
		if pool, err := s.settingSvc.GetCodexTicketPool(loadCtx); err == nil {
			next.pool = pool
		}
		cancel()
	}
	s.settings.Store(next)
	return next
}

// enabled 返回当前全局开关（管理端使用）。
func (s *CodexTicketService) Enabled(ctx context.Context) bool {
	return s.runtimeSettings(ctx).enabled
}

// gatedModel 判断模型是否在打票名单内。
func (s *CodexTicketService) gatedModel(model string) bool {
	model = normalizeCodexTicketModel(model)
	if model == "" {
		return false
	}
	for _, configured := range s.baseConfig().Models {
		if normalizeCodexTicketModel(configured) == model {
			return true
		}
	}
	return false
}

// allowsWithoutTicket 解析全局/账号级「无票放行」。
func (s *CodexTicketService) allowsWithoutTicket(settings *codexTicketRuntimeSettings, account *Account, model string) bool {
	globalDefault := !s.baseConfig().FailClosed
	if settings != nil {
		globalDefault = settings.allowWithoutTicket
	}
	return CodexTicketAllowsWithoutTicket(account, model, globalDefault)
}

// Apply 在出站请求上补齐 x-codex-turn-state：
//   - 全局关闭 / 非打票账号 / 非打票模型 / 客户端已回带 turn-state → 直接返回 nil；
//   - 有可用票据（含过期兜底窗口）→ 注入票据 + Cloudflare 基础设施 Cookie；
//   - 无票且策略禁止裸打 → 返回 ErrCodexTicketUnavailable，由调用方决定重试/换号。
//
// 请求路径不现场打票（打票是后台/管理端行为），避免请求被探测放大。
func (s *CodexTicketService) Apply(ctx context.Context, account *Account, model string, h http.Header) error {
	if s == nil || h == nil || !isCodexTicketAccount(account) {
		return nil
	}
	settings := s.runtimeSettings(ctx)
	if !settings.enabled {
		return nil
	}
	model = normalizeCodexTicketModel(model)
	if !s.gatedModel(model) {
		return nil
	}
	// 官方语义：turn-state 由本回合首个响应铸造，回合内后续请求原样回带。
	// 客户端自己带来的头就是本回合的真实血统，注入会打断回合链。
	if strings.TrimSpace(h.Get(openAICodexTurnStateHeader)) != "" {
		return nil
	}
	ticket := s.lookupTicket(account, model)
	if ticket != nil {
		if usable, reusingExpired := ticket.usable(time.Now()); usable {
			h.Set(openAICodexTurnStateHeader, ticket.State)
			applyCodexTicketCookie(h, ticket)
			s.noteTicketInjection(ticket, reusingExpired)
			return nil
		}
	}
	if s.allowsWithoutTicket(settings, account, model) {
		return nil
	}
	return ErrCodexTicketUnavailable
}

// BlocksAccount 报告账号×模型是否因缺票而不可调度。
// 仅在「全局开启 + 模型在名单 + 无票放行关闭 + 确实没有可用票据」时返回 true。
func (s *CodexTicketService) BlocksAccount(account *Account, model string) bool {
	if s == nil || !isCodexTicketAccount(account) {
		return false
	}
	settings := s.cachedSettings()
	if !settings.enabled {
		return false
	}
	model = normalizeCodexTicketModel(model)
	if !s.gatedModel(model) {
		return false
	}
	if s.allowsWithoutTicket(settings, account, model) {
		return false
	}
	ticket := s.lookupTicket(account, model)
	if ticket == nil {
		return true
	}
	usable, _ := ticket.usable(time.Now())
	return !usable
}

// noteTicketInjection 记录本次注入的票据代数，供 Observe 判断上游是否换票。
func (s *CodexTicketService) noteTicketInjection(ticket *codexTicket, reusingExpired bool) {
	if s == nil || ticket == nil || ticket.GenerationID == "" {
		return
	}
	expiresAt := ticket.ExpiresAt.Add(codexTicketExpiredReuseWindow)
	if reusingExpired {
		expiresAt = time.Now().Add(10 * time.Minute)
	}
	s.injected.Store(codexTicketCacheKey(ticket.AccountID, ticket.Model), codexTicketInjectionSnapshot{
		generationID: ticket.GenerationID,
		state:        ticket.State,
		cookie:       ticket.Cookie,
		expiresAt:    expiresAt,
	})
}

// takeTicketInjection 取出并删除注入快照；过期快照直接丢弃。
func (s *CodexTicketService) takeTicketInjection(accountID int64, model string) *codexTicketInjectionSnapshot {
	key := codexTicketCacheKey(accountID, model)
	raw, ok := s.injected.LoadAndDelete(key)
	if !ok {
		return nil
	}
	snapshot, ok := raw.(codexTicketInjectionSnapshot)
	if !ok || time.Now().After(snapshot.expiresAt) {
		return nil
	}
	return &snapshot
}

// applyCodexTicketCookie 把打票时捕获的 Cloudflare 基础设施 Cookie 与票据一起复用。
// 已有 Cookie 时按追加处理；复用前再过一遍白名单，避免账号/会话 Cookie 混入业务请求。
func applyCodexTicketCookie(h http.Header, ticket *codexTicket) {
	if h == nil || ticket == nil {
		return
	}
	cookie := filterCodexTicketInfrastructureCookies(ticket.Cookie)
	if cookie == "" {
		return
	}
	if existing := filterCodexTicketInfrastructureCookies(h.Get("Cookie")); existing != "" {
		h.Set("Cookie", existing+"; "+cookie)
		return
	}
	h.Set("Cookie", cookie)
}

// codexTicketInfrastructureCookieName 与 codex-rs
// http-client/src/chatgpt_cloudflare_cookies.rs 的 is_allowed_cloudflare_cookie_name 对齐：
// 只复用 Cloudflare 基础设施 Cookie 与 __oailb 路由 Cookie，其余（账号、会话、认证）不透传。
func codexTicketInfrastructureCookieName(name string) bool {
	switch name {
	case "__cf_bm", "__cflb", "__cfruid", "__cfseq", "__cfwaitingroom", "__oailb",
		"_cfuvid", "cf_clearance", "cf_ob_info", "cf_use_ob":
		return true
	}
	return strings.HasPrefix(name, "cf_chl_")
}

// filterCodexTicketInfrastructureCookies 保留 name=value 中的允许项，顺序稳定。
func filterCodexTicketInfrastructureCookies(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	kept := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	for _, pair := range strings.Split(raw, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, value, ok := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || seen[name] || !codexTicketInfrastructureCookieName(name) {
			continue
		}
		seen[name] = true
		kept = append(kept, name+"="+strings.TrimSpace(value))
	}
	return strings.Join(kept, "; ")
}

// extractCodexTicketResponseCookies 从上游 Set-Cookie 中提取允许持久化的基础设施 Cookie。
func extractCodexTicketResponseCookies(h http.Header) string {
	if h == nil || len(h.Values("Set-Cookie")) == 0 {
		return ""
	}
	parsed := (&http.Response{Header: h}).Cookies()
	if len(parsed) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(parsed))
	seen := make(map[string]bool, len(parsed))
	for _, item := range parsed {
		if item == nil || item.Name == "" || seen[item.Name] || !codexTicketInfrastructureCookieName(item.Name) {
			continue
		}
		seen[item.Name] = true
		pairs = append(pairs, item.Name+"="+item.Value)
	}
	return strings.Join(pairs, "; ")
}

// codexOAILBValue 从 Cookie 列表里提取唯一的 __oailb 值；缺失或冲突返回 false。
func codexOAILBValue(cookies []*http.Cookie) (string, bool) {
	value, found := "", false
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name != "__oailb" {
			continue
		}
		if found && value != cookie.Value {
			return "", false
		}
		value, found = cookie.Value, true
	}
	return value, found
}

// codexResponseOAILB 从响应 Set-Cookie 中提取 __oailb 值。
func codexResponseOAILB(h http.Header) (string, bool) {
	if h == nil {
		return "", false
	}
	cookies := make([]*http.Cookie, 0, 2)
	for _, line := range h.Values("Set-Cookie") {
		name, _, _ := strings.Cut(line, "=")
		if strings.TrimSpace(name) != "__oailb" {
			continue
		}
		cookie, err := http.ParseSetCookie(line)
		if err != nil {
			return "", false
		}
		cookies = append(cookies, cookie)
	}
	return codexOAILBValue(cookies)
}

// codexTicketQuotaState 是账号限流窗口的只读视图。
type codexTicketQuotaState struct {
	paused   bool
	resetAt  *time.Time
	resumeAt *time.Time
}

// codexTicketQuota 读取账号级限流窗口；票据探测从不写账号配额状态。
func codexTicketQuota(account *Account, now time.Time) codexTicketQuotaState {
	state := codexTicketQuotaState{}
	if account == nil || account.RateLimitResetAt == nil || !account.RateLimitResetAt.After(now) {
		return state
	}
	resetAt := *account.RateLimitResetAt
	state.resetAt = &resetAt
	state.resumeAt = &resetAt
	state.paused = now.Before(resetAt)
	return state
}

// codexTicketModelRateLimitResetAt 读取 extra.model_rate_limits 中某模型的限流截止时间。
// 兼容两种历史形态：{"model":{"rate_limit_reset_at":"RFC3339"}} 与 {"model":"RFC3339"}。
func codexTicketModelRateLimitResetAt(account *Account, model string) *time.Time {
	if account == nil || account.Extra == nil || model == "" {
		return nil
	}
	limits, ok := account.Extra["model_rate_limits"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := limits[model]
	if !ok {
		return nil
	}
	var text string
	switch value := raw.(type) {
	case string:
		text = value
	case map[string]any:
		if resetAt, ok := value["rate_limit_reset_at"].(string); ok {
			text = resetAt
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &parsed
}

// codexTicketHarvestLimited 报告账号/模型是否处于限流窗口，打票应暂停。
func codexTicketHarvestLimited(account *Account, model string, now time.Time) bool {
	if codexTicketQuota(account, now).paused {
		return true
	}
	if account == nil {
		return false
	}
	resetAt := codexTicketModelRateLimitResetAt(account, model)
	if resetAt == nil || !resetAt.After(now) {
		return false
	}
	if account.Extra["allow_overages"] == true {
		creditsReset := codexTicketModelRateLimitResetAt(account, "AICredits")
		return creditsReset != nil && creditsReset.After(now)
	}
	return true
}

// CodexTicketStatuses 返回账号在全部打票模型上的票据摘要（不含票据本体）。
func (s *CodexTicketService) Statuses(account *Account) []CodexTicketStatus {
	if s == nil || !isCodexTicketAccount(account) {
		return nil
	}
	settings := s.cachedSettings()
	now := time.Now()
	quota := codexTicketQuota(account, now)
	models := s.baseConfig().Models
	out := make([]CodexTicketStatus, 0, len(models))
	for _, model := range models {
		out = append(out, s.ticketStatus(account, model, settings, quota, now))
	}
	return out
}

// TicketStatus 返回单个模型的票据摘要。
func (s *CodexTicketService) TicketStatus(account *Account, model string) CodexTicketStatus {
	if s == nil || !isCodexTicketAccount(account) {
		return CodexTicketStatus{}
	}
	model = normalizeCodexTicketModel(model)
	return s.ticketStatus(account, model, s.cachedSettings(), codexTicketQuota(account, time.Now()), time.Now())
}

func (s *CodexTicketService) ticketStatus(account *Account, model string, settings *codexTicketRuntimeSettings, quota codexTicketQuotaState, now time.Time) CodexTicketStatus {
	base := s.baseConfig()
	status := CodexTicketStatus{
		Model:                model,
		ExpectedTicketLength: codexTicketTargetLength(account, base.TargetLength),
		HarvestPaused:        codexTicketHarvestLimited(account, model, now),
		HarvestEnabled:       settings.enabled && account.Status == StatusActive && CodexTicketParticipationEnabled(account, model),
		ParticipationEnabled: CodexTicketParticipationEnabled(account, model),
		QuotaResetAt:         quota.resetAt,
		HarvestResumeAt:      quota.resumeAt,
	}
	ticket := s.lookupTicket(account, model)
	if ticket != nil {
		usable, reusingExpired := ticket.usable(now)
		status.Ready = usable
		status.ReusingExpired = reusingExpired
		status.Length = ticket.Length
		status.TurnStatePresent = ticket.State != ""
		status.CookiePresent = ticket.Cookie != ""
		capturedAt := ticket.CapturedAt
		status.CapturedAt = &capturedAt
		expiresAt := ticket.ExpiresAt
		status.ExpiresAt = &expiresAt
		if remaining := int64(expiresAt.Sub(now).Seconds()); remaining > 0 {
			status.RemainingSeconds = remaining
		}
	}
	status.Blocked = settings.enabled && s.gatedModel(model) &&
		!s.allowsWithoutTicket(settings, account, model) && !status.Ready
	switch {
	case status.Ready:
		status.Reason = "ready"
	case status.HarvestPaused:
		status.Reason = "rate_limited"
	case !status.ParticipationEnabled:
		status.Reason = "participation_disabled"
	case !settings.enabled:
		status.Reason = "disabled"
	case status.Blocked:
		status.Reason = "ticket_unavailable"
	default:
		status.Reason = "ticket_unavailable"
	}
	return status
}

// Start 启动后台打票循环。重复调用安全；Stop 之后不可重启（与参考实现一致，
// 避免运行期反复建协程带来的状态歧义）。
func (s *CodexTicketService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started || s.stopped {
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	s.wake = make(chan struct{}, 1)
	s.started = true
	go func() {
		defer close(s.done)
		s.harvestLoop(runCtx)
	}()
	logger.LegacyPrintf("service.codex_ticket", "[CodexTicket] harvester started models=%v enabled=%v", s.baseConfig().Models, s.baseConfig().Enabled)
}

// Stop 停止后台打票循环并等待退出。
func (s *CodexTicketService) Stop() {
	if s == nil {
		return
	}
	s.startMu.Lock()
	if s.stopped {
		s.startMu.Unlock()
		return
	}
	s.stopped = true
	cancel, done := s.cancel, s.done
	s.startMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// notifyHarvester 唤醒后台循环立即扫描一轮（失效/手动打票后使用）。
func (s *CodexTicketService) notifyHarvester() {
	if s == nil {
		return
	}
	s.startMu.Lock()
	wake := s.wake
	stopped := s.stopped
	s.startMu.Unlock()
	if stopped || wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// scheduleRetry 记录失败后的下一次尝试时间（区间内抖动）。
func (s *CodexTicketService) scheduleRetry(accountID int64, model string, at time.Time) {
	key := codexTicketCacheKey(accountID, model)
	s.nextRetry.Store(key, at)
}

// retryDue 返回该 key 是否已到下一次尝试时间。
func (s *CodexTicketService) retryDue(accountID int64, model string, now time.Time) bool {
	key := codexTicketCacheKey(accountID, model)
	raw, ok := s.nextRetry.Load(key)
	if !ok {
		return true
	}
	next, ok := raw.(time.Time)
	if !ok {
		return true
	}
	return !now.Before(next)
}
