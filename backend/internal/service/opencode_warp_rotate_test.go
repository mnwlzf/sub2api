package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// openCodeWarpRotateFreeUsageLimitBody 是 opencode 免费层配额耗尽的真实响应体形状。
const openCodeWarpRotateFreeUsageLimitBody = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded. Please try again later."}}`

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// warpRotateEventLog 记录跨替身的事件顺序，用于断言「先轮换、后关空闲连接」。
type warpRotateEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *warpRotateEventLog) add(event string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *warpRotateEventLog) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.events))
	copy(out, l.events)
	return out
}

type warpRotateFakeRotator struct {
	mu       sync.Mutex
	calls    int
	proxyIDs []int64
	proxyURL []string
	exitIP   string
	err      error
	delay    time.Duration
	events   *warpRotateEventLog
}

func (f *warpRotateFakeRotator) EnsureFreshExit(ctx context.Context, proxyID int64, proxyURL string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.proxyIDs = append(f.proxyIDs, proxyID)
	f.proxyURL = append(f.proxyURL, proxyURL)
	delay := f.delay
	err := f.err
	exitIP := f.exitIP
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.events.add("rotate")
	if err != nil {
		return "", err
	}
	if exitIP == "" {
		exitIP = "2a09:test::1"
	}
	return exitIP, nil
}

func (f *warpRotateFakeRotator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *warpRotateFakeRotator) recordedProxyIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.proxyIDs))
	copy(out, f.proxyIDs)
	return out
}

func (f *warpRotateFakeRotator) recordedProxyURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.proxyURL))
	copy(out, f.proxyURL)
	return out
}

// warpRotateUpstreamStub 既提供上游响应，又记录 CloseUpstreamIdleConnections
// 是否被调用（用于断言轮换后必须显式关空闲连接）。
type warpRotateUpstreamStub struct {
	mu        sync.Mutex
	responses []*http.Response
	calls     int
	events    *warpRotateEventLog
	closedFor []string
}

func (s *warpRotateUpstreamStub) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.mu.Lock()
	idx := s.calls
	s.calls++
	var resp *http.Response
	if idx < len(s.responses) {
		resp = s.responses[idx]
	}
	s.mu.Unlock()
	if resp == nil {
		return nil, errors.New("warp rotate test: unexpected upstream call")
	}
	return resp, nil
}

func (s *warpRotateUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, concurrency)
}

func (s *warpRotateUpstreamStub) CloseUpstreamIdleConnections(proxyURL string, accountID int64) {
	s.mu.Lock()
	s.closedFor = append(s.closedFor, proxyURL)
	s.mu.Unlock()
	s.events.add("close_idle")
}

func (s *warpRotateUpstreamStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *warpRotateUpstreamStub) closedProxyURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.closedFor))
	copy(out, s.closedFor)
	return out
}

// warpRotateAccountRepository 只覆盖测试路径会调用的方法。
type warpRotateAccountRepository struct {
	AccountRepository

	mu                  sync.Mutex
	setRateLimitedCalls int
	rateLimitedUntil    []time.Time
}

func (r *warpRotateAccountRepository) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setRateLimitedCalls++
	r.rateLimitedUntil = append(r.rateLimitedUntil, until)
	return nil
}

func (r *warpRotateAccountRepository) rateLimitedCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setRateLimitedCalls
}

// warpRotateRuntimeBlocker 同时扮演调度封禁记录器与「已轮换」判定来源。
type warpRotateRuntimeBlocker struct {
	gateway *OpenAIGatewayService

	mu     sync.Mutex
	blocks []int64
}

func (b *warpRotateRuntimeBlocker) BlockAccountScheduling(account *Account, _ time.Time, _ string) {
	if account == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blocks = append(b.blocks, account.ID)
}

func (b *warpRotateRuntimeBlocker) ClearAccountSchedulingBlock(int64) {}

func (b *warpRotateRuntimeBlocker) ShouldRetryOpenCodeWarpRotated429(ctx context.Context, account *Account, body []byte) bool {
	if b.gateway == nil {
		return false
	}
	return b.gateway.ShouldRetryOpenCodeWarpRotated429(ctx, account, body)
}

func (b *warpRotateRuntimeBlocker) blockCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.blocks)
}

// ---------------------------------------------------------------------------
// 测试夹具
// ---------------------------------------------------------------------------

func openCodeWarpRotateTestConfig(rotate config.GatewayOpenCodeWarpRotateConfig) *config.Config {
	return &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
		Gateway: config.GatewayConfig{
			OpenCodeWarpRotate: rotate,
		},
	}
}

func openCodeWarpRotateTestRotateConfig() config.GatewayOpenCodeWarpRotateConfig {
	return config.GatewayOpenCodeWarpRotateConfig{
		Enabled:                true,
		ServiceURL:             "http://warp-rotate:9110",
		Token:                  "test-token",
		MaxRotationsPerRequest: 2,
	}
}

func openCodeWarpRotateTestAccount(platform string) *Account {
	proxyID := int64(17)
	return &Account{
		ID:          31568,
		Name:        "opencode-go-warp",
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		ProxyID:     &proxyID,
		Proxy: &Proxy{
			ID:       17,
			Name:     "warp-test",
			Protocol: "socks5h",
			Host:     "warp-test",
			Port:     1080,
		},
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"base_url":     "http://upstream.example",
			"api_protocol": APIProtocolResponses,
		},
	}
}

func openCodeWarpRotateTestService(rotate config.GatewayOpenCodeWarpRotateConfig, upstream HTTPUpstream, rotator WarpExitRotator) *OpenAIGatewayService {
	svc := &OpenAIGatewayService{
		cfg:          openCodeWarpRotateTestConfig(rotate),
		httpUpstream: upstream,
	}
	svc.SetWarpExitRotator(rotator)
	return svc
}

func openCodeWarpRotateTestContext(t *testing.T, path string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

// ---------------------------------------------------------------------------
// 1-5：三重限定与开关
// ---------------------------------------------------------------------------

func TestOpenCodeWarpRotate_TriggerMatrix(t *testing.T) {
	cases := []struct {
		name        string
		platform    string
		statusCode  int
		body        []byte
		proxyIDNil  bool
		credentials map[string]any
		rotateCfg   config.GatewayOpenCodeWarpRotateConfig
		wantCalls   int
	}{
		{
			name:       "opencode_go 429 free usage limit rotates once",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  1,
		},
		{
			name:       "openai 429 does not rotate",
			platform:   PlatformOpenAI,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "grok 429 does not rotate",
			platform:   PlatformGrok,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "deepseek 429 does not rotate",
			platform:   PlatformDeepseek,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "opencode_go 400 does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusBadRequest,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "opencode_go 500 does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusInternalServerError,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "opencode_go 403 does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusForbidden,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "opencode_go 429 without FreeUsageLimitError does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`),
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "opencode_go 429 with empty body does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusTooManyRequests,
			body:       nil,
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "proxy id nil does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			proxyIDNil: true,
			rotateCfg:  openCodeWarpRotateTestRotateConfig(),
			wantCalls:  0,
		},
		{
			name:       "global switch off does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  config.GatewayOpenCodeWarpRotateConfig{ServiceURL: "http://warp-rotate:9110"},
			wantCalls:  0,
		},
		{
			name:       "service url missing does not rotate",
			platform:   PlatformOpenCodeGo,
			statusCode: http.StatusTooManyRequests,
			body:       []byte(openCodeWarpRotateFreeUsageLimitBody),
			rotateCfg:  config.GatewayOpenCodeWarpRotateConfig{Enabled: true},
			wantCalls:  0,
		},
		{
			name:        "account credential on overrides global off",
			platform:    PlatformOpenCodeGo,
			statusCode:  http.StatusTooManyRequests,
			body:        []byte(openCodeWarpRotateFreeUsageLimitBody),
			credentials: map[string]any{openCodeWarpRotateCredential: "on"},
			rotateCfg:   config.GatewayOpenCodeWarpRotateConfig{ServiceURL: "http://warp-rotate:9110"},
			wantCalls:   1,
		},
		{
			name:        "account credential off overrides global on",
			platform:    PlatformOpenCodeGo,
			statusCode:  http.StatusTooManyRequests,
			body:        []byte(openCodeWarpRotateFreeUsageLimitBody),
			credentials: map[string]any{openCodeWarpRotateCredential: "off"},
			rotateCfg:   openCodeWarpRotateTestRotateConfig(),
			wantCalls:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rotator := &warpRotateFakeRotator{}
			svc := openCodeWarpRotateTestService(tc.rotateCfg, nil, rotator)
			account := openCodeWarpRotateTestAccount(tc.platform)
			if tc.proxyIDNil {
				account.ProxyID = nil
			}
			for k, v := range tc.credentials {
				account.Credentials[k] = v
			}
			c := openCodeWarpRotateTestContext(t, "/v1/responses")

			_, rotated := svc.rotateOpenCodeWarpExitOn429(context.Background(), c, account, tc.statusCode, tc.body)

			require.Equal(t, tc.wantCalls, rotator.callCount())
			require.Equal(t, tc.wantCalls == 1, rotated)
		})
	}
}

func TestOpenCodeWarpRotate_NoRotatorInjectedIsNoop(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: openCodeWarpRotateTestConfig(openCodeWarpRotateTestRotateConfig())}
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	c := openCodeWarpRotateTestContext(t, "/v1/responses")

	_, rotated := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), c, account, http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
	)

	require.False(t, rotated)
}

// ---------------------------------------------------------------------------
// 6：轮换失败 / 超时不得 panic，必须回落原有 failover 分支
// ---------------------------------------------------------------------------

func TestOpenCodeWarpRotate_RotatorFailureFallsBack(t *testing.T) {
	cases := []struct {
		name    string
		rotator *warpRotateFakeRotator
		ctx     context.Context
	}{
		{
			name:    "rotator returns error",
			rotator: &warpRotateFakeRotator{err: errors.New("no_usable_exit")},
			ctx:     context.Background(),
		},
		{
			name:    "rotator blocks until ctx deadline",
			rotator: &warpRotateFakeRotator{delay: 2 * time.Second},
			ctx:     nil, // 每个子测试单独构造带超时的 ctx
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), nil, tc.rotator)
			account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
			c := openCodeWarpRotateTestContext(t, "/v1/responses")

			ctx := tc.ctx
			if ctx == nil {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
			}

			require.NotPanics(t, func() {
				_, rotated := svc.rotateOpenCodeWarpExitOn429(
					ctx, c, account, http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
				)
				require.False(t, rotated, "failed rotation must not enable same-account retry")
			})
		})
	}
}

// ---------------------------------------------------------------------------
// 7：并发 singleflight 去重
// ---------------------------------------------------------------------------

func TestOpenCodeWarpRotate_Concurrent429CollapsesToOneRotation(t *testing.T) {
	rotator := &warpRotateFakeRotator{delay: 30 * time.Millisecond}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), nil, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)

	const goroutines = 10
	var wg sync.WaitGroup
	results := make([]bool, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := openCodeWarpRotateTestContext(t, "/v1/responses")
			_, rotated := svc.rotateOpenCodeWarpExitOn429(
				context.Background(), c, account, http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
			)
			results[idx] = rotated
		}(i)
	}
	wg.Wait()

	require.Equal(t, 1, rotator.callCount(), "singleflight must collapse concurrent 429 rotations")
	for i, rotated := range results {
		require.True(t, rotated, "goroutine %d must observe the shared successful rotation", i)
	}
	require.Equal(t, []int64{17}, rotator.recordedProxyIDs())
}

func TestOpenCodeWarpRotate_MinIntervalSkipsRepeatRotation(t *testing.T) {
	cfg := openCodeWarpRotateTestRotateConfig()
	cfg.MinIntervalSeconds = 60
	rotator := &warpRotateFakeRotator{}
	svc := openCodeWarpRotateTestService(cfg, nil, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)

	_, first := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), openCodeWarpRotateTestContext(t, "/v1/responses"), account,
		http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
	)
	_, second := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), openCodeWarpRotateTestContext(t, "/v1/responses"), account,
		http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
	)

	require.True(t, first)
	require.False(t, second, "second rotation inside min_interval_seconds must be skipped")
	require.Equal(t, 1, rotator.callCount())
}

func TestOpenCodeWarpRotate_DifferentProxiesRotateIndependently(t *testing.T) {
	rotator := &warpRotateFakeRotator{}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), nil, rotator)
	first := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	second := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	secondProxyID := int64(18)
	second.ProxyID = &secondProxyID
	second.Proxy = &Proxy{ID: 18, Name: "warp-other", Protocol: "socks5h", Host: "warp-test", Port: 1080}

	_, firstRotated := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), openCodeWarpRotateTestContext(t, "/v1/responses"), first,
		http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
	)
	_, secondRotated := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), openCodeWarpRotateTestContext(t, "/v1/responses"), second,
		http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
	)

	require.True(t, firstRotated)
	require.True(t, secondRotated)
	require.Equal(t, 2, rotator.callCount())
	require.ElementsMatch(t, []int64{17, 18}, rotator.recordedProxyIDs())
}

// ---------------------------------------------------------------------------
// 8：/responses 收口点——failover 错误携带同账号重试预算与顺序保证
// ---------------------------------------------------------------------------

func TestOpenCodeWarpRotate_ForwardMarksSameAccountRetryAndClosesIdleConnections(t *testing.T) {
	events := &warpRotateEventLog{}
	rotator := &warpRotateFakeRotator{exitIP: "2a09:rotated::1", events: events}
	upstream := &warpRotateUpstreamStub{
		events: events,
		responses: []*http.Response{
			{
				StatusCode: http.StatusTooManyRequests,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Retry-After":  []string{"49711"},
				},
				Body: io.NopCloser(strings.NewReader(openCodeWarpRotateFreeUsageLimitBody)),
			},
		},
	}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), upstream, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	c := openCodeWarpRotateTestContext(t, "/v1/responses")
	body := []byte(`{"model":"mimo-v2.6-flash-free","input":"hello","stream":false}`)

	result, err := svc.Forward(context.Background(), c, account, body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount, "rotation success must opt into same-account retry")
	require.Equal(t, 2, failoverErr.SameAccountRetryMax)
	require.Equal(t, openCodeWarpRotateRetryDelay, failoverErr.SameAccountRetryDelay)
	require.True(t, failoverErr.SameAccountRetryDeadline.IsZero(),
		"a non-zero deadline would bypass the retry-count cap in handler/failover_loop.go")
	require.Equal(t, "49711", failoverErr.ResponseHeaders.Get("Retry-After"))
	require.Equal(t, 1, rotator.callCount())
	require.Equal(t, []int64{17}, rotator.recordedProxyIDs())
	require.Equal(t, []string{"socks5h://warp-test:1080"}, rotator.recordedProxyURLs())
	require.Equal(t, []string{"socks5h://warp-test:1080"}, upstream.closedProxyURLs())
	require.Equal(t, []string{"rotate", "close_idle"}, events.snapshot(),
		"idle connections must be closed after the rotation, never before")
}

func TestOpenCodeWarpRotate_ForwardWithoutRotationKeepsLegacyFailover(t *testing.T) {
	events := &warpRotateEventLog{}
	upstream := &warpRotateUpstreamStub{
		events: events,
		responses: []*http.Response{{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_exceeded"}}`)),
		}},
	}
	rotator := &warpRotateFakeRotator{events: events}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), upstream, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	c := openCodeWarpRotateTestContext(t, "/v1/responses")
	body := []byte(`{"model":"mimo-v2.6-flash-free","input":"hello","stream":false}`)

	_, err := svc.Forward(context.Background(), c, account, body)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Zero(t, failoverErr.SameAccountRetryMax)
	require.Zero(t, rotator.callCount())
	require.Empty(t, upstream.closedProxyURLs())
}

// ---------------------------------------------------------------------------
// 9：CC 收口点（failoverOpenAIUpstreamHTTPError）
// ---------------------------------------------------------------------------

func TestOpenCodeWarpRotate_CCPipelineMarksSameAccountRetry(t *testing.T) {
	events := &warpRotateEventLog{}
	rotator := &warpRotateFakeRotator{events: events}
	upstream := &warpRotateUpstreamStub{events: events}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), upstream, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	c := openCodeWarpRotateTestContext(t, "/v1/chat/completions")
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"49711"},
		},
	}

	failoverErr := svc.failoverOpenAIUpstreamHTTPError(
		context.Background(), c, account, resp, []byte(openCodeWarpRotateFreeUsageLimitBody), "Rate limit exceeded", "mimo-v2.6-flash-free",
	)

	require.NotNil(t, failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 2, failoverErr.SameAccountRetryMax)
	require.Equal(t, "49711", failoverErr.ResponseHeaders.Get("Retry-After"))
	require.Equal(t, 1, rotator.callCount())
	require.Equal(t, []string{"socks5h://warp-test:1080"}, upstream.closedProxyURLs())
	require.Equal(t, []string{"rotate", "close_idle"}, events.snapshot())
}

func TestOpenCodeWarpRotate_CCPipelineIgnoresOtherPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformDeepseek, PlatformZhipu} {
		t.Run(platform, func(t *testing.T) {
			rotator := &warpRotateFakeRotator{}
			svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), &warpRotateUpstreamStub{}, rotator)
			account := openCodeWarpRotateTestAccount(platform)
			c := openCodeWarpRotateTestContext(t, "/v1/chat/completions")
			resp := &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}

			svc.failoverOpenAIUpstreamHTTPError(
				context.Background(), c, account, resp, []byte(openCodeWarpRotateFreeUsageLimitBody), "Rate limit exceeded", "some-model",
			)

			require.Zero(t, rotator.callCount())
		})
	}
}

// ---------------------------------------------------------------------------
// 10：handle429 冷却抑制——已轮换的请求必须保持账号可调度
// ---------------------------------------------------------------------------

func TestOpenCodeWarpRotate_Suppresses429CooldownOnlyWhenRotated(t *testing.T) {
	repo := &warpRotateAccountRepository{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimits}
	blocker := &warpRotateRuntimeBlocker{gateway: svc}
	rateLimits.SetAccountRuntimeBlocker(blocker)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	body := []byte(openCodeWarpRotateFreeUsageLimitBody)

	rotatedCtx := context.WithValue(context.Background(), openCodeWarpRotatedContextKey{}, true)
	require.True(t, svc.ShouldRetryOpenCodeWarpRotated429(rotatedCtx, account, body))
	rateLimits.handle429(rotatedCtx, account, http.Header{}, body)

	require.Zero(t, repo.rateLimitedCallCount(), "rotated request must not persist a 429 cooldown")
	require.Zero(t, blocker.blockCount(), "rotated request must stay schedulable for the same-account retry")

	// 未轮换（无标记）时保持原有行为：写冷却 + 摘号。
	require.False(t, svc.ShouldRetryOpenCodeWarpRotated429(context.Background(), account, body))
	rateLimits.handle429(context.Background(), account, http.Header{}, body)

	require.Equal(t, 1, repo.rateLimitedCallCount())
	require.Equal(t, 1, blocker.blockCount())

	// 非 opencode_go 平台即使带标记也不抑制。
	other := openCodeWarpRotateTestAccount(PlatformDeepseek)
	rateLimits.handle429(rotatedCtx, other, http.Header{}, body)
	require.Equal(t, 2, repo.rateLimitedCallCount())
}

// TestOpenCodeWarpRotate_MarkerSurvivesToHandle429 覆盖标记的真实传播链路：
// rotateOpenCodeWarpExitOn429 → handleOpenAIAccountUpstreamError
// → openAIAccountStateContext(context.WithoutCancel) → HandleUpstreamError → handle429。
// 任一环节丢失 ctx value，同账号重试都会因为账号被摘出调度快照而静默失效。
func TestOpenCodeWarpRotate_MarkerSurvivesToHandle429(t *testing.T) {
	repo := &warpRotateAccountRepository{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rotator := &warpRotateFakeRotator{}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), nil, rotator)
	svc.rateLimitService = rateLimits
	blocker := &warpRotateRuntimeBlocker{gateway: svc}
	rateLimits.SetAccountRuntimeBlocker(blocker)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	body := []byte(openCodeWarpRotateFreeUsageLimitBody)
	c := openCodeWarpRotateTestContext(t, "/v1/responses")

	rotateCtx, rotated := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), c, account, http.StatusTooManyRequests, body,
	)
	require.True(t, rotated)
	require.Equal(t, 1, rotator.callCount())

	svc.handleOpenAIAccountUpstreamError(rotateCtx, account, http.StatusTooManyRequests, http.Header{}, body)

	require.Zero(t, repo.rateLimitedCallCount(),
		"the rotated marker must survive context.WithoutCancel and suppress the 429 cooldown")
	require.Zero(t, blocker.blockCount(),
		"the rotated account must stay in the scheduling snapshot for the same-account retry")
}
