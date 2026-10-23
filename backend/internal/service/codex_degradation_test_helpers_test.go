package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// codexProbeTestAccount 是号原生探测测试用的 OpenAI OAuth 账号：
// chatgpt_account_id + chatgpt_user_id 构成稳定的 Codex 身份命名空间。
func codexProbeTestAccount() *Account {
	return &Account{
		ID:       41,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":       "tok",
			"chatgpt_account_id": "acc-41",
			"chatgpt_user_id":    "user-41",
		},
	}
}

type codexDegradationTokenStub struct {
	token string
	err   error
}

func (s codexDegradationTokenStub) GetAccessToken(context.Context, *Account) (string, error) {
	return s.token, s.err
}

type codexDegradationSetCall struct {
	accountID int64
	until     time.Time
	reason    string
}

// codexDegradationRepoStub 模拟 accounts 表：SetTempUnschedulable 保持与生产
// SQL 一致的"单调只延长"语义，便于断言降级续期行为。
type codexDegradationRepoStub struct {
	AccountRepository

	mu            sync.Mutex
	accounts      map[int64]*Account
	setCalls      []codexDegradationSetCall
	clearCalls    []int64
	extraUpdates  []int64
	dueAccounts   []Account
	listCallCount int
}

func newCodexDegradationRepoStub(accounts ...*Account) *codexDegradationRepoStub {
	repo := &codexDegradationRepoStub{accounts: map[int64]*Account{}}
	for _, account := range accounts {
		repo.accounts[account.ID] = account
	}
	return repo
}

func (r *codexDegradationRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	clone := *account
	clone.Extra = mergeMap(nil, account.Extra)
	return &clone, nil
}

func (r *codexDegradationRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCalls = append(r.setCalls, codexDegradationSetCall{accountID: id, until: until, reason: reason})
	account := r.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	// 单调只延长：与生产 SQL 的 WHERE temp_unschedulable_until IS NULL OR < $1 一致。
	if account.TempUnschedulableUntil == nil || account.TempUnschedulableUntil.Before(until) {
		account.TempUnschedulableUntil = &until
		account.TempUnschedulableReason = reason
	}
	return nil
}

func (r *codexDegradationRepoStub) ClearTempUnschedulable(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearCalls = append(r.clearCalls, id)
	account := r.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	account.TempUnschedulableUntil = nil
	account.TempUnschedulableReason = ""
	return nil
}

func (r *codexDegradationRepoStub) ClearModelRateLimits(_ context.Context, _ int64) error {
	return nil
}

func (r *codexDegradationRepoStub) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	r.extraUpdates = append(r.extraUpdates, id)
	return nil
}

func (r *codexDegradationRepoStub) ListDueCodexDegradationCheckAccounts(_ context.Context, _ time.Time, limit int) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCallCount++
	result := make([]Account, 0, len(r.dueAccounts))
	for _, account := range r.dueAccounts {
		if len(result) >= limit {
			break
		}
		result = append(result, account)
	}
	return result, nil
}

func (r *codexDegradationRepoStub) storedState(t *testing.T, id int64) *CodexDegradationCheckState {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		t.Fatalf("account %d not found in stub", id)
	}
	state := decodeCodexDegradationCheckState(account.Extra)
	if state == nil {
		t.Fatalf("expected persisted check state for account %d", id)
	}
	return state
}

func (r *codexDegradationRepoStub) hasStoredState(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return false
	}
	return decodeCodexDegradationCheckState(account.Extra) != nil
}

type codexDegradationProbeCall struct {
	accountID int64
	model     string
}

type codexDegradationProbeStub struct {
	mu    sync.Mutex
	calls []codexDegradationProbeCall
	next  func(accountID int64, model string) (CodexDiagnosticVerdict, error)
}

func (p *codexDegradationProbeStub) ProbeCodexDegradation(_ context.Context, accountID int64, model string) (CodexDiagnosticVerdict, error) {
	p.mu.Lock()
	p.calls = append(p.calls, codexDegradationProbeCall{accountID: accountID, model: model})
	handler := p.next
	p.mu.Unlock()
	return handler(accountID, model)
}

func (p *codexDegradationProbeStub) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *codexDegradationProbeStub) probedIDs() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]int64, 0, len(p.calls))
	for _, call := range p.calls {
		ids = append(ids, call.accountID)
	}
	return ids
}

func fixedCodexDegradationVerdict(status string) *codexDegradationProbeStub {
	return &codexDegradationProbeStub{
		next: func(_ int64, _ string) (CodexDiagnosticVerdict, error) {
			return CodexDiagnosticVerdict{Status: status}, nil
		},
	}
}

func newCodexDegradationTestService(repo AccountRepository, settingRepo SettingRepository, deps CodexDegradationDeps) *CodexDegradationService {
	deps.AccountRepo = repo
	if deps.Settings == nil {
		deps.Settings = NewSettingService(settingRepo, &config.Config{})
	}
	if deps.Tokens == nil {
		deps.Tokens = codexDegradationTokenStub{token: "tok"}
	}
	svc := NewCodexDegradationService(deps)
	return svc
}

func enabledCodexDegradationSettings() map[string]string {
	return map[string]string{
		SettingKeyCodexDegradation: `{"enabled":true,"interval_minutes":10,"block_minutes":30,"models":["gpt-6.1-sol"]}`,
	}
}

func activeOpenAIOAuthAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
	}
}
