package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// bpsStateWriterStub 记录错误处置写入，用于验证 handler 的分类接线。
type bpsStateWriterStub struct {
	mu sync.Mutex

	rateLimitCalls []bpsRateLimitCall
	modelCalls     []bpsModelCooldownCall
	authCalls      []bpsAuthFailureCall

	authWritten bool
	err         error
}

type bpsRateLimitCall struct {
	accountID int64
	until     time.Time
}

type bpsModelCooldownCall struct {
	accountID int64
	model     string
	until     time.Time
}

type bpsAuthFailureCall struct {
	accountID int64
	token     string
	workspace string
	code      string
}

func (s *bpsStateWriterStub) SetOpenAIBPSRateLimit(_ context.Context, accountID int64, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateLimitCalls = append(s.rateLimitCalls, bpsRateLimitCall{accountID: accountID, until: until})
	return s.err
}

func (s *bpsStateWriterStub) SetOpenAIBPSModelCooldown(_ context.Context, accountID int64, model string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelCalls = append(s.modelCalls, bpsModelCooldownCall{accountID: accountID, model: model, until: until})
	return s.err
}

func (s *bpsStateWriterStub) MarkOpenAIBPSAuthFailure(_ context.Context, accountID int64, accessToken, accountIDHeader, errorCode string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authCalls = append(s.authCalls, bpsAuthFailureCall{
		accountID: accountID, token: accessToken, workspace: accountIDHeader, code: errorCode,
	})
	return s.authWritten, s.err
}

func (s *bpsStateWriterStub) calls() (rate, model, auth int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rateLimitCalls), len(s.modelCalls), len(s.authCalls)
}

func TestClassifyOpenAIBPSError(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name   string
		input  OpenAIBPSErrorInput
		verify func(t *testing.T, action OpenAIBPSErrorAction)
	}{
		{
			name:  "401 without code is revoked",
			input: OpenAIBPSErrorInput{StatusCode: http.StatusUnauthorized, Now: now},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindAuth, action.Kind)
				require.True(t, action.DisableAccount)
				require.Equal(t, OpenAIBPSDefaultAuthErrorCode, action.AuthErrorCode)
				require.False(t, action.Retryable, "401 不重试")
				require.True(t, action.HasStateWrite())
			},
		},
		{
			name:  "401 keeps upstream code",
			input: OpenAIBPSErrorInput{StatusCode: http.StatusUnauthorized, Code: "invalid_token", Now: now},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, "invalid_token", action.AuthErrorCode)
				require.Equal(t, "invalid_token", action.Code)
				require.True(t, action.DisableAccount)
			},
		},
		{
			name: "403 model access changed cools mapped model",
			input: OpenAIBPSErrorInput{
				StatusCode: http.StatusForbidden,
				Code:       "basispoints_model_access_changed",
				Model:      "gpt-6-astra-upstream",
				Now:        now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindModelNotAllowed, action.Kind)
				require.Equal(t, OpenAIBPSModelNotAllowedCode, action.Code)
				require.Equal(t, "gpt-6-astra-upstream", action.ModelCooldownScope)
				require.Equal(t, now.Add(OpenAIBPSModelNotAllowedCooldown), action.ModelCooldownUntil)
				require.False(t, action.DisableAccount, "模型权限错误不整号失效")
				require.False(t, action.Retryable)
				require.True(t, action.HasStateWrite())
			},
		},
		{
			name: "403 model_not_allowed in message",
			input: OpenAIBPSErrorInput{
				StatusCode: http.StatusForbidden,
				Message:    "model_not_allowed: this workspace has no access",
				Model:      "gpt-6-astra",
				Now:        now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindModelNotAllowed, action.Kind)
				require.Equal(t, OpenAIBPSModelNotAllowedCode, action.Code)
			},
		},
		{
			name:  "ordinary 403 does not disable account",
			input: OpenAIBPSErrorInput{StatusCode: http.StatusForbidden, Code: "forbidden", Now: now},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindForbidden, action.Kind)
				require.False(t, action.DisableAccount)
				require.False(t, action.HasStateWrite(), "普通 403 不写任何冷却")
				require.False(t, action.Retryable)
				require.Equal(t, "forbidden", action.Code)
			},
		},
		{
			name:  "ordinary 403 fallback code",
			input: OpenAIBPSErrorInput{StatusCode: http.StatusForbidden, Now: now},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSForbiddenCode, action.Code)
			},
		},
		{
			name:  "400 is not retried",
			input: OpenAIBPSErrorInput{StatusCode: http.StatusBadRequest, Code: "invalid_request_error", Now: now},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindInvalidRequest, action.Kind)
				require.False(t, action.Retryable)
				require.False(t, action.HasStateWrite())
			},
		},
		{
			name: "429 honors retry-after",
			input: OpenAIBPSErrorInput{
				StatusCode: http.StatusTooManyRequests,
				RetryAfter: "30",
				Now:        now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindRateLimited, action.Kind)
				require.Equal(t, now.Add(30*time.Second), action.RateLimitUntil)
				require.True(t, action.Retryable, "未绑定新会话可换号")
				require.Equal(t, OpenAIBPSRateLimitedCode, action.Code)
			},
		},
		{
			name: "429 without retry-after uses default",
			input: OpenAIBPSErrorInput{
				StatusCode: http.StatusTooManyRequests,
				Now:        now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, now.Add(OpenAIBPSRateLimitDefaultCooldown), action.RateLimitUntil)
			},
		},
		{
			name: "429 bound session cannot failover",
			input: OpenAIBPSErrorInput{
				StatusCode:   http.StatusTooManyRequests,
				RetryAfter:   "30",
				SessionBound: true,
				Now:          now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.False(t, action.Retryable)
				require.Equal(t, now.Add(30*time.Second), action.RateLimitUntil, "冷却照常写入")
			},
		},
		{
			name: "500 unbound new session may failover",
			input: OpenAIBPSErrorInput{
				StatusCode: http.StatusInternalServerError,
				Now:        now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindUpstream, action.Kind)
				require.True(t, action.Retryable)
				require.False(t, action.HasStateWrite())
			},
		},
		{
			name: "500 bound session cannot failover",
			input: OpenAIBPSErrorInput{
				StatusCode:   http.StatusBadGateway,
				SessionBound: true,
				Now:          now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.False(t, action.Retryable)
			},
		},
		{
			name: "500 output already started cannot failover",
			input: OpenAIBPSErrorInput{
				StatusCode:    http.StatusServiceUnavailable,
				OutputStarted: true,
				Now:           now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.False(t, action.Retryable)
			},
		},
		{
			name: "network error unbound new session may failover",
			input: OpenAIBPSErrorInput{
				StatusCode: 0,
				Now:        now,
			},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindTransport, action.Kind)
				require.Equal(t, OpenAIBPSUpstreamUnreachableCode, action.Code)
				require.True(t, action.Retryable)
				require.False(t, action.HasStateWrite())
			},
		},
		{
			name:  "404 is single failure",
			input: OpenAIBPSErrorInput{StatusCode: http.StatusNotFound, Now: now},
			verify: func(t *testing.T, action OpenAIBPSErrorAction) {
				require.Equal(t, OpenAIBPSErrorKindUnknown, action.Kind)
				require.False(t, action.Retryable)
				require.False(t, action.HasStateWrite())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.verify(t, ClassifyOpenAIBPSError(tt.input))
		})
	}
}

func TestParseOpenAIBPSRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cooldown, ok := ParseOpenAIBPSRetryAfter("30", now)
	require.True(t, ok)
	require.Equal(t, 30*time.Second, cooldown)

	cooldown, ok = ParseOpenAIBPSRetryAfter(now.Add(45*time.Second).Format(http.TimeFormat), now)
	require.True(t, ok)
	require.Equal(t, 45*time.Second, cooldown)

	cooldown, ok = ParseOpenAIBPSRetryAfter("", now)
	require.False(t, ok)
	require.Zero(t, cooldown)

	cooldown, ok = ParseOpenAIBPSRetryAfter("soon", now)
	require.False(t, ok)
	require.Zero(t, cooldown)

	cooldown, ok = ParseOpenAIBPSRetryAfter("-5", now)
	require.False(t, ok)
	require.Zero(t, cooldown)

	cooldown, ok = ParseOpenAIBPSRetryAfter("999999", now)
	require.True(t, ok)
	require.Equal(t, OpenAIBPSRateLimitMaxCooldown, cooldown, "超大 Retry-After 必须截断")
}

// 401：条件写入认证失败，并停止该账号调度（凭证状态 revoked）。
func TestOpenAIBPSHandlerAuthFailureStopsScheduling(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	account := bpsTestAccount(7, "token-7", "ws-7")
	state := &bpsStateWriterStub{authWritten: true}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return account, nil
		}},
		Sessions:     store,
		AccountState: state,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusUnauthorized, "application/json", "req_401",
				`{"error":{"code":"token_revoked","message":"token revoked"}}`), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusUnauthorized, response.Code)
	rateCalls, modelCalls, authCalls := state.calls()
	require.Zero(t, rateCalls)
	require.Zero(t, modelCalls)
	require.Equal(t, 1, authCalls)
	require.Equal(t, bpsAuthFailureCall{
		accountID: 7, token: "token-7", workspace: "ws-7", code: "token_revoked",
	}, state.authCalls[0])
	require.Equal(t, OpenAIBPSCredentialStatusRevoked,
		ResolveOpenAIBPSCredentialState(account, time.Now()).Status,
		"写入成功后内存账号也应停止调度")
}

// 模型权限错误：只冷却映射后的模型，账号其它模型仍可用。
func TestOpenAIBPSHandlerModelNotAllowedCoolsMappedModel(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	account := bpsTestAccount(7, "token-7", "ws-7")
	account.Credentials["model_mapping"] = map[string]any{
		"gpt-6-astra": "gpt-6-astra-upstream",
		"gpt-5.6-sol": "gpt-5.6-sol-upstream",
	}
	state := &bpsStateWriterStub{}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return account, nil
		}},
		Sessions:     store,
		AccountState: state,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusForbidden, "application/json", "req_model",
				`{"error":{"code":"basispoints_model_access_changed","message":"model access changed"}}`), nil
		}),
	})
	before := time.Now()
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusForbidden, response.Code)
	rateCalls, modelCalls, authCalls := state.calls()
	require.Zero(t, rateCalls)
	require.Zero(t, authCalls)
	require.Equal(t, 1, modelCalls)
	call := state.modelCalls[0]
	require.Equal(t, int64(7), call.accountID)
	require.Equal(t, "gpt-6-astra-upstream", call.model, "冷却 scope 必须是映射后的上游模型")
	require.WithinDuration(t, before.Add(OpenAIBPSModelNotAllowedCooldown), call.until, 5*time.Second)
	require.True(t, account.IsSchedulable(), "模型级冷却不影响账号其它模型")
	require.False(t, openAIBPSAccountSelectable(account, "gpt-6-astra"), "该模型应被选号器排除")
	require.True(t, openAIBPSAccountSelectable(account, "gpt-5.6-sol"), "其它模型仍可选")
}

// 普通 403：单次失败，不整号失效、不写冷却。
func TestOpenAIBPSHandlerOrdinaryForbiddenKeepsAccount(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	account := bpsTestAccount(7, "token-7", "ws-7")
	state := &bpsStateWriterStub{}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return account, nil
		}},
		Sessions:     store,
		AccountState: state,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusForbidden, "application/json", "req_403",
				`{"error":{"code":"forbidden","message":"not allowed"}}`), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusForbidden, response.Code)
	rateCalls, modelCalls, authCalls := state.calls()
	require.Zero(t, rateCalls+modelCalls+authCalls, "普通 403 不得写任何账号状态")
	require.True(t, account.IsSchedulable())
	require.NotEqual(t, OpenAIBPSCredentialStatusRevoked,
		ResolveOpenAIBPSCredentialState(account, time.Now()).Status)
}

// 400：不重试、不冷却。
func TestOpenAIBPSHandlerBadRequestDoesNotRetry(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	state := &bpsStateWriterStub{}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions:     store,
		AccountState: state,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusBadRequest, "application/json", "req_400",
				`{"error":{"code":"invalid_request_error","message":"bad input"}}`), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusBadRequest, response.Code)
	rateCalls, modelCalls, authCalls := state.calls()
	require.Zero(t, rateCalls+modelCalls+authCalls)
	require.False(t, ClassifyOpenAIBPSError(OpenAIBPSErrorInput{StatusCode: http.StatusBadRequest}).Retryable)
}

// 429：按 Retry-After 冷却账号，并把 Retry-After 透传给客户端。
func TestOpenAIBPSHandlerRateLimitUsesRetryAfter(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	state := &bpsStateWriterStub{}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions:     store,
		AccountState: state,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			resp := bpsTestResponse(http.StatusTooManyRequests, "application/json", "req_429",
				`{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`)
			resp.Header.Set("Retry-After", "30")
			return resp, nil
		}),
	})
	before := time.Now()
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusTooManyRequests, response.Code)
	require.Equal(t, "30", response.Header().Get("Retry-After"))
	rateCalls, modelCalls, authCalls := state.calls()
	require.Zero(t, modelCalls+authCalls)
	require.Equal(t, 1, rateCalls)
	require.Equal(t, int64(7), state.rateLimitCalls[0].accountID)
	require.WithinDuration(t, before.Add(30*time.Second), state.rateLimitCalls[0].until, 5*time.Second)
}

// 模型级冷却：选号器必须排除处于冷却窗口的模型。
func TestOpenAIBPSAccountSelectorRespectsModelCooldown(t *testing.T) {
	account := bpsTestAccount(7, "token-7", "ws-7")
	account.Extra = map[string]any{
		modelRateLimitsKey: map[string]any{
			"gpt-6-astra": map[string]any{
				"rate_limit_reset_at": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
				"reason":              OpenAIBPSModelNotAllowedCode,
			},
		},
	}
	require.False(t, openAIBPSAccountSelectable(account, "gpt-6-astra"))
	require.True(t, openAIBPSAccountSelectable(account, "gpt-5.6-sol"))

	// 冷却到期后恢复可选。
	account.Extra[modelRateLimitsKey] = map[string]any{
		"gpt-6-astra": map[string]any{
			"rate_limit_reset_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		},
	}
	require.True(t, openAIBPSAccountSelectable(account, "gpt-6-astra"))
}

type bpsStateRepoStub struct {
	rateLimited  []bpsRateLimitCall
	modelLimited []bpsModelCooldownCall
}

func (s *bpsStateRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	s.rateLimited = append(s.rateLimited, bpsRateLimitCall{accountID: id, until: resetAt})
	return nil
}

func (s *bpsStateRepoStub) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, _ ...string) error {
	s.modelLimited = append(s.modelLimited, bpsModelCooldownCall{accountID: id, model: scope, until: resetAt})
	return nil
}

type bpsCredentialWriterStub struct {
	calls []bpsAuthFailureCall
}

func (s *bpsCredentialWriterStub) MarkOpenAIBPSAuthFailure(_ context.Context, accountID int64, accessToken, accountIDHeader, errorCode string) (bool, error) {
	s.calls = append(s.calls, bpsAuthFailureCall{
		accountID: accountID, token: accessToken, workspace: accountIDHeader, code: errorCode,
	})
	return true, nil
}

func TestOpenAIBPSAccountStateStore(t *testing.T) {
	repo := &bpsStateRepoStub{}
	credentials := &bpsCredentialWriterStub{}
	store := NewOpenAIBPSAccountStateStore(repo, credentials)
	ctx := context.Background()
	until := time.Now().Add(30 * time.Minute)

	require.NoError(t, store.SetOpenAIBPSRateLimit(ctx, 7, until))
	require.Len(t, repo.rateLimited, 1)
	require.Equal(t, int64(7), repo.rateLimited[0].accountID)
	require.Equal(t, until, repo.rateLimited[0].until)

	require.NoError(t, store.SetOpenAIBPSModelCooldown(ctx, 7, "gpt-6-astra", until))
	require.Len(t, repo.modelLimited, 1)
	require.Equal(t, "gpt-6-astra", repo.modelLimited[0].model)

	written, err := store.MarkOpenAIBPSAuthFailure(ctx, 7, "token", "workspace", "token_revoked")
	require.NoError(t, err)
	require.True(t, written)
	require.Len(t, credentials.calls, 1)
	require.Equal(t, "token", credentials.calls[0].token)

	// 未接线时全部是 no-op，绝不 panic。
	empty := NewOpenAIBPSAccountStateStore(nil, nil)
	require.NoError(t, empty.SetOpenAIBPSRateLimit(ctx, 7, until))
	require.NoError(t, empty.SetOpenAIBPSModelCooldown(ctx, 7, "m", until))
	written, err = empty.MarkOpenAIBPSAuthFailure(ctx, 7, "token", "workspace", "code")
	require.NoError(t, err)
	require.False(t, written)
}
