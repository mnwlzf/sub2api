package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type rerankHandlerAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *rerankHandlerAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r *rerankHandlerAccountRepo) accountsForPlatform(platform string) []service.Account {
	out := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out
}

func (r *rerankHandlerAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *rerankHandlerAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *rerankHandlerAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

// ListModelAvailabilityCandidates feeds the no-account diagnosis path
// (classifyNoAccountErrorFromGin) when the scheduler finds no candidate.
func (r *rerankHandlerAccountRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, _ []string, _ bool) ([]service.Account, error) {
	return r.accounts, nil
}

// rerankHandlerUpstream is an HTTPUpstream stub that answers per account ID and
// records the requests it received.
type rerankHandlerUpstream struct {
	service.HTTPUpstream
	mu      sync.Mutex
	calls   []int64
	paths   []string
	bodies  [][]byte
	respond func(accountID int64) *http.Response
}

func (u *rerankHandlerUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	u.paths = append(u.paths, req.URL.Path)
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
		u.bodies = append(u.bodies, body)
	}
	respond := u.respond
	u.mu.Unlock()
	return respond(accountID), nil
}

func rerankJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func newRerankTestRouter(t *testing.T, accounts []service.Account, upstream service.HTTPUpstream) *gin.Engine {
	t.Helper()

	groupID := int64(9401)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxAccountSwitches = 3

	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)

	repo := &rerankHandlerAccountRepo{accounts: accounts}
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	concurrency := service.NewConcurrencyService(&concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	})
	h := NewOpenAIGatewayHandler(
		gateway,
		concurrency,
		billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, nil, cfg,
	)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
			ID:      9402,
			GroupID: &groupID,
			User:    &service.User{ID: 9403, Status: service.StatusActive},
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
		})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9403, Concurrency: 2})
		c.Next()
	})
	RegisterRerankRoutes(router.Group("/v1"), h)
	return router
}

func rerankAPIKeyAccount(id int64, baseURL string) service.Account {
	return service.Account{
		ID:          id,
		Name:        "rerank-apikey",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-rerank",
			"base_url": baseURL,
		},
	}
}

func TestOpenAIGatewayHandler_Rerank_MissingModelReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &rerankHandlerUpstream{respond: func(int64) *http.Response {
		return rerankJSONResponse(http.StatusOK, `{"results":[]}`)
	}}
	router := newRerankTestRouter(t, []service.Account{rerankAPIKeyAccount(1, "https://api.jina.ai")}, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"query":"q","documents":["a"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "model is required", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Empty(t, upstream.calls)
}

func TestOpenAIGatewayHandler_Rerank_Passthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamBody := `{"id":"rerank-1","results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":5,"total_tokens":5}}`
	upstream := &rerankHandlerUpstream{respond: func(int64) *http.Response {
		return rerankJSONResponse(http.StatusOK, upstreamBody)
	}}
	router := newRerankTestRouter(t, []service.Account{rerankAPIKeyAccount(11, "https://api.jina.ai")}, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"model":"rerank-v1","query":"q","documents":["a","b"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, upstreamBody, rec.Body.String())
	require.Equal(t, []int64{11}, upstream.calls)
	require.Equal(t, []string{"/v1/rerank"}, upstream.paths)
	require.Equal(t, "rerank-v1", gjson.GetBytes(upstream.bodies[0], "model").String())
}

func TestOpenAIGatewayHandler_Rerank_UpstreamErrorPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamBody := `{"error":{"type":"invalid_request_error","code":"invalid_model","message":"model not supported"}}`
	upstream := &rerankHandlerUpstream{respond: func(int64) *http.Response {
		return rerankJSONResponse(http.StatusBadRequest, upstreamBody)
	}}
	router := newRerankTestRouter(t, []service.Account{rerankAPIKeyAccount(21, "https://api.jina.ai")}, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"model":"bad-model","query":"q","documents":["a"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, upstreamBody, rec.Body.String())
	require.Equal(t, []int64{21}, upstream.calls)
}

func TestOpenAIGatewayHandler_Rerank_NoCapableAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &rerankHandlerUpstream{respond: func(int64) *http.Response {
		return rerankJSONResponse(http.StatusOK, `{"results":[]}`)
	}}
	// OAuth 账号不支持 rerank 能力，调度器必须直接拒绝而不是把请求打到
	// 不存在的上游端点。
	oauthAccount := service.Account{
		ID:          31,
		Name:        "oauth-only",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	router := newRerankTestRouter(t, []service.Account{oauthAccount}, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"model":"rerank-v1","query":"q","documents":["a"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// 账号池里有账号但没有账号声明 rerank 能力：调度层无候选，分类器走
	// 503 兜底（不是 model_not_found，因为模型本身在池内是"支持"的）。
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "api_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "Service temporarily unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Empty(t, upstream.calls)
}

func TestOpenAIGatewayHandler_Rerank_FailoverToSecondAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamBody := `{"results":[{"index":0,"relevance_score":0.8}]}`
	upstream := &rerankHandlerUpstream{respond: func(accountID int64) *http.Response {
		if accountID == 41 {
			return rerankJSONResponse(http.StatusInternalServerError, `{"error":{"type":"server_error","message":"boom"}}`)
		}
		return rerankJSONResponse(http.StatusOK, upstreamBody)
	}}
	router := newRerankTestRouter(t, []service.Account{
		rerankAPIKeyAccount(41, "https://api.jina.ai"),
		rerankAPIKeyAccount(42, "https://api.jina.ai"),
	}, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"model":"rerank-v1","query":"q","documents":["a"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, upstreamBody, rec.Body.String())
	require.Equal(t, []int64{41, 42}, upstream.calls)
}

func TestOpenAIGatewayHandler_Rerank_PlatformGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &rerankHandlerUpstream{respond: func(int64) *http.Response {
		return rerankJSONResponse(http.StatusOK, `{"results":[]}`)
	}}
	groupID := int64(9401)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		&rerankHandlerAccountRepo{}, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
			ID:      9404,
			GroupID: &groupID,
			User:    &service.User{ID: 9405},
			Group:   &service.Group{ID: groupID, Platform: service.PlatformAnthropic},
		})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9405})
		c.Next()
	})
	RegisterRerankRoutes(router.Group("/v1"), h)

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"model":"rerank-v1","query":"q","documents":["a"]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "not_found_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Empty(t, upstream.calls)
}
