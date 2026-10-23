package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 本文件是 Codex 打票核心链路的定向单测：
// 票据形状（长度/前缀）、注入仅在没有回带时发生、Cookie 白名单、无票门控、
// 代理池轮换、主动打票落库、观测捕获、失效去重、挑战生成、节奏校验。

const codexTicketTestModel = "gpt-5.6-sol"

// testCodexTicketState 生成指定长度的合法形状票据（gAAAAA 前缀 + 'A' 填充）。
func testCodexTicketState(length int) string {
	if length < len(CodexTicketStatePrefix) {
		length = len(CodexTicketStatePrefix)
	}
	return CodexTicketStatePrefix + strings.Repeat("A", length-len(CodexTicketStatePrefix))
}

// ---------- 测试替身 ----------

type codexTicketTestAccountRepo struct {
	AccountRepository
	mu        sync.Mutex
	accounts  map[int64]*Account
	updates   []map[string]any
	updateIDs []int64
}

func newCodexTicketTestAccountRepo(accounts ...*Account) *codexTicketTestAccountRepo {
	repo := &codexTicketTestAccountRepo{accounts: make(map[int64]*Account, len(accounts))}
	for _, account := range accounts {
		repo.accounts[account.ID] = account
	}
	return repo
}

func (r *codexTicketTestAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account, ok := r.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func (r *codexTicketTestAccountRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if platform != "" && account.Platform != platform {
			continue
		}
		result = append(result, *account)
	}
	return result, nil
}

func (r *codexTicketTestAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	account, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	r.updates = append(r.updates, updates)
	r.updateIDs = append(r.updateIDs, id)
	return nil
}

type codexTicketTestSettingRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func newCodexTicketTestSettingRepo(values map[string]string) *codexTicketTestSettingRepo {
	if values == nil {
		values = map[string]string{}
	}
	return &codexTicketTestSettingRepo{values: values}
}

func (r *codexTicketTestSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *codexTicketTestSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *codexTicketTestSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *codexTicketTestSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *codexTicketTestSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *codexTicketTestSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *codexTicketTestSettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

type codexTicketTestProxyRepo struct {
	ProxyRepository
	proxies []Proxy
}

func (r *codexTicketTestProxyRepo) ListActive(_ context.Context) ([]Proxy, error) {
	return append([]Proxy(nil), r.proxies...), nil
}

func (r *codexTicketTestProxyRepo) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	wanted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	result := make([]Proxy, 0, len(ids))
	for _, proxy := range r.proxies {
		if wanted[proxy.ID] {
			result = append(result, proxy)
		}
	}
	return result, nil
}

type codexTicketTestAttempts struct {
	CodexTicketAttemptRepository
	mu       sync.Mutex
	inserted []*CodexTicketAttempt
}

func (r *codexTicketTestAttempts) Insert(_ context.Context, attempt *CodexTicketAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt.ID = int64(len(r.inserted) + 1)
	copied := *attempt
	r.inserted = append(r.inserted, &copied)
	return nil
}

func (r *codexTicketTestAttempts) List(_ context.Context, _ int64, _ string, _ bool, _ int, _ int) ([]CodexTicketAttempt, int64, error) {
	return nil, 0, nil
}

func (r *codexTicketTestAttempts) Cleanup(context.Context) error { return nil }

func (r *codexTicketTestAttempts) TryLock(context.Context, int64, string) (func(), bool, error) {
	return func() {}, true, nil
}

func (r *codexTicketTestAttempts) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inserted)
}

type codexTicketTestLifecycle struct {
	CodexTicketLifecycleRepository
	mu                sync.Mutex
	events            []*CodexTicketInvalidation
	currentGeneration string
}

func (r *codexTicketTestLifecycle) Invalidate(_ context.Context, event *CodexTicketInvalidation) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.events {
		if existing.TicketGenerationID == event.TicketGenerationID {
			return false, nil
		}
	}
	event.InvalidatedCurrent = r.currentGeneration != "" && r.currentGeneration == event.TicketGenerationID
	copied := *event
	r.events = append(r.events, &copied)
	return true, nil
}

func (r *codexTicketTestLifecycle) eventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *codexTicketTestLifecycle) lastEvent() *CodexTicketInvalidation {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return nil
	}
	return r.events[len(r.events)-1]
}

type codexTicketTestUpstream struct {
	HTTPUpstream
	mu        sync.Mutex
	requests  []*http.Request
	responses []*http.Response
	errs      []error
}

func (u *codexTicketTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, req)
	index := len(u.requests) - 1
	if index < len(u.errs) && u.errs[index] != nil {
		return nil, u.errs[index]
	}
	if index >= len(u.responses) {
		return nil, errors.New("unexpected upstream call")
	}
	return u.responses[index], nil
}

// ---------- 构造辅助 ----------

type codexTicketTestEnv struct {
	service   *CodexTicketService
	accounts  *codexTicketTestAccountRepo
	settings  *codexTicketTestSettingRepo
	proxies   *codexTicketTestProxyRepo
	attempts  *codexTicketTestAttempts
	lifecycle *codexTicketTestLifecycle
	upstream  *codexTicketTestUpstream
}

func newCodexTicketTestEnv(t *testing.T, accounts ...*Account) *codexTicketTestEnv {
	t.Helper()
	env := &codexTicketTestEnv{
		accounts:  newCodexTicketTestAccountRepo(accounts...),
		settings:  newCodexTicketTestSettingRepo(nil),
		proxies:   &codexTicketTestProxyRepo{proxies: []Proxy{{ID: 1, Name: "proxy-1", Protocol: "http", Host: "127.0.0.1", Port: 18080}}},
		attempts:  &codexTicketTestAttempts{},
		lifecycle: &codexTicketTestLifecycle{},
		upstream:  &codexTicketTestUpstream{},
	}
	settingService := &SettingService{settingRepo: env.settings, proxyRepo: env.proxies}
	env.service = NewCodexTicketService(CodexTicketDeps{
		AccountRepo: env.accounts,
		SettingSvc:  settingService,
		ProxyRepo:   env.proxies,
		Cfg:         &config.Config{},
		Upstream:    env.upstream,
		Attempts:    env.attempts,
		Lifecycle:   env.lifecycle,
	})
	env.applySettings(t, config.CodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProbeIntervalSeconds:  6,
		HarvestAttemptTimeoutSeconds: 5,
		HarvestRetryMinSeconds:       10,
		HarvestRetryMaxSeconds:       30,
		HarvestRefreshSeconds:        1800,
		Models:                       []string{codexTicketTestModel},
	}, true)
	return env
}

// applySettings 直接写入进程内设置快照，避免单测依赖全局 viper。
func (e *codexTicketTestEnv) applySettings(t *testing.T, cfg config.CodexTicketConfig, allowWithoutTicket bool) {
	t.Helper()
	snapshot := &codexTicketRuntimeSettings{
		cfg:                config.NormalizeCodexTicketConfig(cfg),
		enabled:            cfg.Enabled,
		allowWithoutTicket: allowWithoutTicket,
		cadence: CodexTicketCadence{
			RetryMinSeconds: cfg.HarvestRetryMinSeconds,
			RetryMaxSeconds: cfg.HarvestRetryMaxSeconds,
			RefreshSeconds:  cfg.HarvestRefreshSeconds,
		},
		pool:     CodexTicketProxyPool{Mode: CodexTicketPoolModeAll, ProxyIDs: []int64{}},
		loadedAt: time.Now(),
	}
	e.service.baseSettings.Store(snapshot)
	e.service.settings.Store(snapshot)
}

func newCodexTicketTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":       "test-access-token",
			"chatgpt_account_id": "chatgpt-account",
		},
		Extra: map[string]any{},
	}
}

// seedTicket 把票据直接写入账号 extra（模拟已打票状态）。
func seedTicket(account *Account, model, state, cookie, generation string, expiresAt time.Time) *codexTicket {
	ticket := &codexTicket{
		AccountID:          account.ID,
		Model:              model,
		GenerationID:       generation,
		VerificationMethod: codexTicketVerificationShape,
		State:              state,
		Cookie:             cookie,
		Length:             len(state),
		CapturedAt:         time.Now().Add(-time.Minute),
		ExpiresAt:          expiresAt,
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[codexTicketExtraKey(model)] = ticket
	return ticket
}

// ---------- 票据形状 ----------

func TestCodexTicketStateShapeValidation(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	team := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "Team"}}
	business := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "selfserve_business_prolite"}}

	require.True(t, codexTicketStateValid(oauth, 292, testCodexTicketState(292)))
	require.False(t, codexTicketStateValid(oauth, 292, testCodexTicketState(291)))
	require.False(t, codexTicketStateValid(oauth, 292, "AAAAAA"+strings.Repeat("A", 286)))
	require.Equal(t, 332, codexTicketTargetLength(team, 292))
	require.Equal(t, 332, codexTicketTargetLength(business, 292))
	require.True(t, codexTicketStateValid(team, 292, testCodexTicketState(332)))
	require.False(t, codexTicketStateValid(team, 292, testCodexTicketState(292)))
	require.Equal(t, 292, codexTicketTargetLength(oauth, 0))
}

// ---------- 注入 ----------

func TestCodexTicketApplyInjectsOnlyWhenAbsent(t *testing.T) {
	account := newCodexTicketTestAccount(11)
	env := newCodexTicketTestEnv(t, account)
	state := testCodexTicketState(292)
	seedTicket(account, codexTicketTestModel, state, "__cf_bm=cf; __oailb=route", "gen-1", time.Now().Add(time.Hour))

	header := http.Header{}
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, header))
	require.Equal(t, state, header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__cf_bm=cf; __oailb=route", header.Get("Cookie"))

	// 客户端已回带 turn-state：绝不覆盖（那是本回合真实血统）。
	echoed := http.Header{}
	echoed.Set(openAICodexTurnStateHeader, testCodexTicketState(292)[:291]+"B")
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, echoed))
	require.NotEqual(t, state, echoed.Get(openAICodexTurnStateHeader))

	// 非打票模型 / 非 OAuth 账号 / 全局关闭：一律不注入。
	otherHeader := http.Header{}
	require.NoError(t, env.service.Apply(context.Background(), account, "gpt-4.1", otherHeader))
	require.Empty(t, otherHeader.Get(openAICodexTurnStateHeader))
	require.NoError(t, env.service.Apply(context.Background(), &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, codexTicketTestModel, http.Header{}))
}

func TestCodexTicketApplyWithoutTicketHonorsPolicy(t *testing.T) {
	account := newCodexTicketTestAccount(12)
	env := newCodexTicketTestEnv(t, account)

	// 默认 allow_without_ticket=true → 返回 nil，不注入。
	header := http.Header{}
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, header))
	require.Empty(t, header.Get(openAICodexTurnStateHeader))

	// 关闭放行 → 无票请求返回 ErrCodexTicketUnavailable。
	env.applySettings(t, config.CodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, Models: []string{codexTicketTestModel},
	}, false)
	require.ErrorIs(t, env.service.Apply(context.Background(), account, codexTicketTestModel, http.Header{}), ErrCodexTicketUnavailable)

	// 账号级覆盖优先于全局：显式允许无票 → 放行。
	account.Extra[codexTicketAllowWithoutTicketKey] = true
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, http.Header{}))
}

func TestCodexTicketApplyCookieWhitelist(t *testing.T) {
	account := newCodexTicketTestAccount(13)
	env := newCodexTicketTestEnv(t, account)
	seedTicket(account, codexTicketTestModel, testCodexTicketState(292),
		"__cf_bm=cf; session_token=secret; __oailb=route; _cfuvid=uvid", "gen-1", time.Now().Add(time.Hour))

	header := http.Header{}
	header.Set("Cookie", "existing_account_cookie=keep-me")
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, header))
	cookie := header.Get("Cookie")
	require.Contains(t, cookie, "__cf_bm=cf")
	require.Contains(t, cookie, "__oailb=route")
	require.Contains(t, cookie, "_cfuvid=uvid")
	require.NotContains(t, cookie, "session_token")
	// 非白名单 Cookie 即使已在请求里也不允许透传。
	require.NotContains(t, cookie, "existing_account_cookie")
}

// ---------- 门控 ----------

func TestCodexTicketBlocksAccount(t *testing.T) {
	account := newCodexTicketTestAccount(14)
	env := newCodexTicketTestEnv(t, account)
	env.applySettings(t, config.CodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, Models: []string{codexTicketTestModel},
	}, false)

	require.True(t, env.service.BlocksAccount(account, codexTicketTestModel))
	seedTicket(account, codexTicketTestModel, testCodexTicketState(292), "", "gen-1", time.Now().Add(time.Hour))
	require.False(t, env.service.BlocksAccount(account, codexTicketTestModel))

	// 非打票模型、未参与打票的账号、全局关闭都不门控。
	require.False(t, env.service.BlocksAccount(account, "gpt-4.1"))
	require.False(t, env.service.BlocksAccount(&Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, codexTicketTestModel))
	env.applySettings(t, config.CodexTicketConfig{Enabled: false, Models: []string{codexTicketTestModel}}, false)
	require.False(t, env.service.BlocksAccount(account, codexTicketTestModel))
}

func TestCodexTicketStatuses(t *testing.T) {
	account := newCodexTicketTestAccount(16)
	env := newCodexTicketTestEnv(t, account)
	statuses := env.service.Statuses(account)
	require.Len(t, statuses, 1)
	require.Equal(t, codexTicketTestModel, statuses[0].Model)
	require.Equal(t, 292, statuses[0].ExpectedTicketLength)
	require.False(t, statuses[0].Ready)

	seedTicket(account, codexTicketTestModel, testCodexTicketState(292), "__cf_bm=cf", "gen-1", time.Now().Add(time.Hour))
	status := env.service.TicketStatus(account, codexTicketTestModel)
	require.True(t, status.Ready)
	require.Equal(t, 292, status.Length)
	require.True(t, status.TurnStatePresent)
	require.True(t, status.CookiePresent)
	require.Greater(t, status.RemainingSeconds, int64(0))
}

// ---------- 代理池 ----------

func TestCodexTicketProxyPoolRotation(t *testing.T) {
	account := newCodexTicketTestAccount(17)
	env := newCodexTicketTestEnv(t, account)
	env.proxies.proxies = []Proxy{
		{ID: 1, Name: "p1", Protocol: "http", Host: "10.0.0.1", Port: 8001},
		{ID: 2, Name: "p2", Protocol: "http", Host: "10.0.0.2", Port: 8002},
		{ID: 3, Name: "p3", Protocol: "http", Host: "10.0.0.3", Port: 8003},
	}
	// custom 模式只轮换 1、2。
	require.NoError(t, env.settings.Set(context.Background(), CodexTicketSettingProxyPool, `{"mode":"custom","proxy_ids":[1,2]}`))

	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		proxyURL, proxy, err := env.service.chooseProxy(context.Background(), account.ID, codexTicketTestModel)
		require.NoError(t, err)
		require.NotNil(t, proxy)
		require.Contains(t, []int64{1, 2}, proxy.ID)
		require.NotContains(t, proxyURL, "10.0.0.3")
		seen[proxyURL] = true
	}
	require.Len(t, seen, 2, "轮换应覆盖池内全部代理")

	// all 模式包含全部未过期代理。
	require.NoError(t, env.settings.Set(context.Background(), CodexTicketSettingProxyPool, `{"mode":"all"}`))
	available, err := env.service.settingSvc.AvailableCodexTicketProxies(context.Background())
	require.NoError(t, err)
	require.Len(t, available, 3)

	// 空池 → ErrCodexTicketNoProxy。
	env.proxies.proxies = nil
	_, _, err = env.service.chooseProxy(context.Background(), account.ID, codexTicketTestModel)
	require.ErrorIs(t, err, ErrCodexTicketNoProxy)
}

// ---------- 主动打票 ----------

func codexTicketTestProbeResponse(state, cookie string, status int) *http.Response {
	header := http.Header{}
	if state != "" {
		header.Set(openAICodexTurnStateHeader, state)
	}
	if cookie != "" {
		for _, item := range strings.Split(cookie, "|") {
			header.Add("Set-Cookie", item)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"1 2 3 4 5\"}\n\ndata: [DONE]\n\n")),
	}
}

func TestCodexTicketManualHarvestStoresTicket(t *testing.T) {
	account := newCodexTicketTestAccount(21)
	env := newCodexTicketTestEnv(t, account)
	state := testCodexTicketState(292)
	env.upstream.responses = []*http.Response{
		codexTicketTestProbeResponse(state, "__cf_bm=cf|__oailb=route|session=secret", http.StatusOK),
	}

	result, err := env.service.ManualHarvest(context.Background(), account.ID, codexTicketTestModel)
	require.NoError(t, err)
	require.Equal(t, codexTicketOutcomeSuccess, result.Outcome)
	require.NotNil(t, result.HTTPStatus)
	require.Equal(t, http.StatusOK, *result.HTTPStatus)
	require.NotNil(t, result.TicketGenerationID)
	require.True(t, result.HistoryRecorded)
	require.Equal(t, 1, env.attempts.count())
	require.True(t, result.TicketStatus.Ready)

	stored := parseCodexTicket(account, codexTicketTestModel, account.Extra[codexTicketExtraKey(codexTicketTestModel)])
	require.NotNil(t, stored)
	require.Equal(t, state, stored.State)
	require.Equal(t, "__cf_bm=cf; __oailb=route", stored.Cookie)
	require.Equal(t, 292, stored.Length)

	// 探测请求形状：Authorization + Codex 身份头 + 挑战体。
	require.Len(t, env.upstream.requests, 1)
	probe := env.upstream.requests[0]
	require.Equal(t, "Bearer test-access-token", probe.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-account", probe.Header.Get("chatgpt-account-id"))
	require.NotEmpty(t, probe.Header.Get("x-codex-turn-metadata"))
	require.NotEmpty(t, probe.Header.Get("version"))
}

func TestCodexTicketManualHarvestTeamAccountUses332(t *testing.T) {
	account := newCodexTicketTestAccount(22)
	account.Credentials["plan_type"] = "team"
	env := newCodexTicketTestEnv(t, account)
	state := testCodexTicketState(332)
	env.upstream.responses = []*http.Response{codexTicketTestProbeResponse(state, "", http.StatusOK)}

	result, err := env.service.ManualHarvest(context.Background(), account.ID, codexTicketTestModel)
	require.NoError(t, err)
	require.Equal(t, codexTicketOutcomeSuccess, result.Outcome)
	require.Equal(t, 332, *result.TicketLength)
}

func TestCodexTicketManualHarvestRejectsWrongShape(t *testing.T) {
	account := newCodexTicketTestAccount(23)
	env := newCodexTicketTestEnv(t, account)
	env.upstream.responses = []*http.Response{codexTicketTestProbeResponse(testCodexTicketState(100), "", http.StatusOK)}

	result, err := env.service.ManualHarvest(context.Background(), account.ID, codexTicketTestModel)
	require.NoError(t, err)
	require.Equal(t, codexTicketOutcomeMiss, result.Outcome)
	require.Equal(t, "ticket_shape", result.ReasonCode)
	require.Nil(t, parseCodexTicket(account, codexTicketTestModel, account.Extra[codexTicketExtraKey(codexTicketTestModel)]))
	require.Equal(t, 1, env.attempts.count())
}

func TestCodexTicketManualHarvest429StillAcceptsTicket(t *testing.T) {
	account := newCodexTicketTestAccount(24)
	env := newCodexTicketTestEnv(t, account)
	state := testCodexTicketState(292)
	env.upstream.responses = []*http.Response{codexTicketTestProbeResponse(state, "__cf_bm=cf", http.StatusTooManyRequests)}

	result, err := env.service.ManualHarvest(context.Background(), account.ID, codexTicketTestModel)
	require.NoError(t, err)
	require.Equal(t, codexTicketOutcomeSuccess, result.Outcome)
	require.Equal(t, http.StatusTooManyRequests, *result.HTTPStatus)
}

func TestCodexTicketManualHarvestUnavailablePaths(t *testing.T) {
	account := newCodexTicketTestAccount(25)
	env := newCodexTicketTestEnv(t, account)

	// 非打票模型。
	_, err := env.service.ManualHarvest(context.Background(), account.ID, "gpt-4.1")
	require.ErrorIs(t, err, ErrCodexTicketModel)

	// 全局关闭。
	env.applySettings(t, config.CodexTicketConfig{Enabled: false, Models: []string{codexTicketTestModel}}, true)
	_, err = env.service.ManualHarvest(context.Background(), account.ID, codexTicketTestModel)
	require.ErrorIs(t, err, ErrCodexTicketUnavailable)

	// 账号处于限流窗口。
	env.applySettings(t, config.CodexTicketConfig{Enabled: true, Models: []string{codexTicketTestModel}}, true)
	resetAt := time.Now().Add(time.Hour)
	account.RateLimitResetAt = &resetAt
	_, err = env.service.ManualHarvest(context.Background(), account.ID, codexTicketTestModel)
	require.ErrorIs(t, err, ErrCodexTicketRateLimited)
}

// ---------- 观测 ----------

func TestCodexTicketObserveCapturesTicket(t *testing.T) {
	account := newCodexTicketTestAccount(31)
	env := newCodexTicketTestEnv(t, account)
	state := testCodexTicketState(292)
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, state)
	header.Set("Set-Cookie", "__cf_bm=cf; account_session=secret")

	env.service.ObserveResponse(context.Background(), account, codexTicketTestModel, http.StatusOK, "/v1/responses", header)

	stored := parseCodexTicket(account, codexTicketTestModel, account.Extra[codexTicketExtraKey(codexTicketTestModel)])
	require.NotNil(t, stored)
	require.Equal(t, state, stored.State)
	require.Equal(t, "__cf_bm=cf", stored.Cookie, "只允许持久化基础设施 Cookie")
	require.NotEmpty(t, stored.GenerationID)

	// 同一代票据再次观测不应重复写库。
	updatesBefore := len(env.accounts.updates)
	env.service.Observe(context.Background(), account, codexTicketTestModel, header)
	require.Equal(t, updatesBefore, len(env.accounts.updates))
}

func TestCodexTicketObserveInvalidationDeduped(t *testing.T) {
	account := newCodexTicketTestAccount(32)
	env := newCodexTicketTestEnv(t, account)
	original := testCodexTicketState(292)
	seedTicket(account, codexTicketTestModel, original, "__cf_bm=cf; __oailb=route-a", "gen-1", time.Now().Add(time.Hour))
	env.lifecycle.currentGeneration = "gen-1"

	// Apply 注入票据并记录注入快照。
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, http.Header{}))

	returned := testCodexTicketState(292)
	returned = returned[:len(returned)-1] + "B"
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, returned)
	header.Set("Set-Cookie", "__oailb=route-b; Path=/")

	env.service.ObserveResponse(context.Background(), account, codexTicketTestModel, http.StatusOK, "/v1/responses", header)
	require.Equal(t, 1, env.lifecycle.eventCount())
	event := env.lifecycle.lastEvent()
	require.Equal(t, "gen-1", event.TicketGenerationID)
	require.Equal(t, CodexTicketInvalidationCredentialsChanged, event.ReasonCode)
	require.Equal(t, original, *event.OriginalTicket)
	require.Equal(t, returned, event.ReturnedTicket)
	require.True(t, event.InvalidatedCurrent)

	// 注入快照已消费：再次观测同一响应不会重复记事件（服务层去重）。
	env.service.ObserveResponse(context.Background(), account, codexTicketTestModel, http.StatusOK, "/v1/responses", header)
	require.Equal(t, 1, env.lifecycle.eventCount())

	// 新票据被捕获（上游返回的新一代票据应替换被失效的票据）。
	stored := parseCodexTicket(account, codexTicketTestModel, account.Extra[codexTicketExtraKey(codexTicketTestModel)])
	require.NotNil(t, stored)
	require.Equal(t, returned, stored.State)
}

func TestCodexTicketObserveNoInvalidationWhenOAILBSame(t *testing.T) {
	account := newCodexTicketTestAccount(33)
	env := newCodexTicketTestEnv(t, account)
	seedTicket(account, codexTicketTestModel, testCodexTicketState(292), "__oailb=route-a", "gen-1", time.Now().Add(time.Hour))
	env.lifecycle.currentGeneration = "gen-1"
	require.NoError(t, env.service.Apply(context.Background(), account, codexTicketTestModel, http.Header{}))

	returned := testCodexTicketState(292)
	returned = returned[:len(returned)-1] + "C"
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, returned)
	header.Set("Set-Cookie", "__oailb=route-a; Path=/") // 路由 Cookie 未变 → 不是失效证据

	env.service.ObserveResponse(context.Background(), account, codexTicketTestModel, http.StatusOK, "/v1/responses", header)
	require.Zero(t, env.lifecycle.eventCount())
}

// ---------- 挑战 / 节奏 ----------

func TestCodexTicketProbeChallenge(t *testing.T) {
	challenge, err := newCodexTicketProbeChallenge()
	require.NoError(t, err)
	require.GreaterOrEqual(t, challenge.ExpectedCount, codexTicketChallengeMinCount)
	require.LessOrEqual(t, challenge.ExpectedCount, codexTicketChallengeMaxCount)
	require.Contains(t, challenge.Prompt, "1 到 355")
	require.GreaterOrEqual(t, codexTicketCountChallengeNumbers("1 2 3 400 355"), 4)
	require.Equal(t, 0, codexTicketCountChallengeNumbers("没有数字"))
}

func TestCodexTicketCadenceValidation(t *testing.T) {
	require.NoError(t, ValidateCodexTicketCadence(CodexTicketCadence{RetryMinSeconds: 10, RetryMaxSeconds: 30, RefreshSeconds: 1800}))
	require.Error(t, ValidateCodexTicketCadence(CodexTicketCadence{RetryMinSeconds: 30, RetryMaxSeconds: 10, RefreshSeconds: 1800}))
	require.Error(t, ValidateCodexTicketCadence(CodexTicketCadence{RetryMinSeconds: 0, RetryMaxSeconds: 10}))
	require.Error(t, ValidateCodexTicketCadence(CodexTicketCadence{RetryMinSeconds: 1, RetryMaxSeconds: 1, RefreshSeconds: -1}))
}

// ---------- 设置扩展 ----------

func TestSettingServiceCodexTicketPoolAndCadence(t *testing.T) {
	settingRepo := newCodexTicketTestSettingRepo(nil)
	proxyRepo := &codexTicketTestProxyRepo{proxies: []Proxy{
		{ID: 1, Name: "p1", Protocol: "http", Host: "10.0.0.1", Port: 8001},
		{ID: 2, Name: "p2", Protocol: "http", Host: "10.0.0.2", Port: 8002, Status: StatusActive},
	}}
	settingService := &SettingService{settingRepo: settingRepo, proxyRepo: proxyRepo}
	ctx := context.Background()

	pool, err := settingService.GetCodexTicketPool(ctx)
	require.NoError(t, err)
	require.Equal(t, CodexTicketPoolModeAll, pool.Mode)

	require.NoError(t, settingService.SetCodexTicketPool(ctx, CodexTicketProxyPool{Mode: CodexTicketPoolModeCustom, ProxyIDs: []int64{2, 1}}))
	pool, err = settingService.GetCodexTicketPool(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, pool.ProxyIDs)
	require.Error(t, settingService.SetCodexTicketPool(ctx, CodexTicketProxyPool{Mode: CodexTicketPoolModeCustom, ProxyIDs: []int64{99}}))

	require.NoError(t, settingService.SetCodexTicketCadence(ctx, CodexTicketCadence{RetryMinSeconds: 5, RetryMaxSeconds: 15, RefreshSeconds: 900}))
	require.Equal(t, CodexTicketCadence{RetryMinSeconds: 5, RetryMaxSeconds: 15, RefreshSeconds: 900},
		settingService.GetCodexTicketCadence(ctx, CodexTicketCadence{}))
	require.Error(t, settingService.SetCodexTicketCadence(ctx, CodexTicketCadence{RetryMinSeconds: 0}))

	require.NoError(t, settingService.SetCodexTicketEnabled(ctx, true))
	require.True(t, settingService.GetCodexTicketEnabled(ctx, false))
	require.NoError(t, settingService.SetCodexTicketAllowWithoutTicket(ctx, true))
	require.True(t, settingService.GetCodexTicketAllowWithoutTicket(ctx, false))
}

// ---------- 未使用但需要引用的辅助 ----------

func TestCodexTicketHelperExports(t *testing.T) {
	account := newCodexTicketTestAccount(41)
	seedTicket(account, codexTicketTestModel, testCodexTicketState(292), "", "gen-1", time.Now().Add(time.Hour))
	require.True(t, CodexTicketReadyModels(account)[codexTicketTestModel])
	require.True(t, IsCodexTicketExtraKey(codexTicketExtraKey(codexTicketTestModel)))
	require.False(t, IsCodexTicketExtraKey("codex_other"))

	merged := MergeCodexTicketExtra(map[string]any{"codex_turn_ticket:x": "stale", "new": 1}, account.Extra)
	require.NotContains(t, merged, "codex_turn_ticket:x")
	require.Contains(t, merged, codexTicketExtraKey(codexTicketTestModel))
	require.Equal(t, 1, merged["new"])

	redacted := RedactCodexTicketExtra(account.Extra)
	require.NotContains(t, redacted, codexTicketExtraKey(codexTicketTestModel))
}
