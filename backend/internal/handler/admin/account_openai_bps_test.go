package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// openAIBPSTestAdminService 在 stubAdminService 基础上支持自定义账号与错误。
type openAIBPSTestAdminService struct {
	*stubAdminService
	account *service.Account
	err     error
}

func (s *openAIBPSTestAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.account != nil {
		account := *s.account
		account.ID = id
		return &account, nil
	}
	return s.stubAdminService.GetAccount(context.Background(), id)
}

func newOpenAIBPSTestAccount() *service.Account {
	return &service.Account{
		ID:       42,
		Name:     "bps-account",
		Platform: service.PlatformOpenAIBPS,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "bps-access-token",
			"chatgpt_account_id": "acct-bps-1",
		},
	}
}

// newOpenAIBPSTestRouter 构造仅包含 BPS 路由的测试路由器。
func newOpenAIBPSTestRouter(accounts openAIBPSAccountReader) (*gin.Engine, *OpenAIBPSAccountHandler) {
	gin.SetMode(gin.TestMode)
	handler := NewOpenAIBPSAccountHandler(accounts)
	router := gin.New()
	admin := router.Group("/admin")
	RegisterOpenAIBPSAdminRoutes(admin, handler)
	return router, handler
}

func decodeOpenAIBPSTestResponse(t *testing.T, recorder *httptest.ResponseRecorder) openAIBPSTestConnectionResponse {
	t.Helper()
	var envelope struct {
		Data openAIBPSTestConnectionResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope.Data
}

func TestOpenAIBPSTestConnectionRejectsInvalidAccountID(t *testing.T) {
	router, _ := newOpenAIBPSTestRouter(&openAIBPSTestAdminService{stubAdminService: newStubAdminService()})

	for _, target := range []string{
		"/admin/accounts/not-a-number/openai-bps/test-connection",
		"/admin/accounts/0/openai-bps/test-connection",
		"/admin/accounts/-3/openai-bps/test-connection",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, target, nil))
		require.Equal(t, http.StatusBadRequest, recorder.Code, target)
	}
}

func TestOpenAIBPSTestConnectionRejectsMalformedBody(t *testing.T) {
	router, _ := newOpenAIBPSTestRouter(&openAIBPSTestAdminService{stubAdminService: newStubAdminService()})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		bytes.NewBufferString(`{"model":`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestOpenAIBPSTestConnectionRejectsNonBPSAccount(t *testing.T) {
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		account: &service.Account{
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeOAuth,
		},
	}
	router, handler := newOpenAIBPSTestRouter(accounts)
	called := false
	handler.runTest = func(context.Context, *service.Account, string, bool) service.OpenAIBPSTestResult {
		called = true
		return service.OpenAIBPSTestResult{}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		bytes.NewBufferString(`{}`),
	))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.False(t, called, "非 BPS 账号不得触发上游探测")
}

func TestOpenAIBPSTestConnectionAccountNotFound(t *testing.T) {
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		err:              service.ErrAccountNotFound,
	}
	router, _ := newOpenAIBPSTestRouter(accounts)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/404/openai-bps/test-connection",
		bytes.NewBufferString(`{}`),
	))
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestOpenAIBPSTestConnectionSuccess(t *testing.T) {
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		account:          newOpenAIBPSTestAccount(),
	}
	router, handler := newOpenAIBPSTestRouter(accounts)
	var gotModel string
	var gotCompact bool
	handler.runTest = func(
		_ context.Context,
		account *service.Account,
		model string,
		compact bool,
	) service.OpenAIBPSTestResult {
		require.Equal(t, service.PlatformOpenAIBPS, account.Platform)
		gotModel, gotCompact = model, compact
		return service.OpenAIBPSTestResult{
			Model:         model,
			Compact:       compact,
			HTTPStatus:    http.StatusOK,
			UpstreamModel: "gpt-6-astra",
			RequestID:     "req-bps-1",
			DurationMS:    123,
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		bytes.NewBufferString(`{"model":"gpt-6-astra","compact":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "gpt-6-astra", gotModel)
	require.True(t, gotCompact)
	data := decodeOpenAIBPSTestResponse(t, recorder)
	require.Equal(t, int64(42), data.AccountID)
	require.Equal(t, "gpt-6-astra", data.Model)
	require.True(t, data.Compact)
	require.Equal(t, http.StatusOK, data.HTTPStatus)
	require.Equal(t, "gpt-6-astra", data.UpstreamModel)
	require.Equal(t, "req-bps-1", data.RequestID)
	require.Empty(t, data.ErrorCode)
	require.Equal(t, 123, data.DurationMS)
}

func TestOpenAIBPSTestConnectionAcceptsEmptyBody(t *testing.T) {
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		account:          newOpenAIBPSTestAccount(),
	}
	router, handler := newOpenAIBPSTestRouter(accounts)
	called := false
	handler.runTest = func(
		_ context.Context,
		_ *service.Account,
		model string,
		compact bool,
	) service.OpenAIBPSTestResult {
		called = true
		require.Empty(t, model, "缺省模型交给 service 层填默认值")
		require.False(t, compact)
		return service.OpenAIBPSTestResult{Model: "gpt-6-astra"}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		nil,
	))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, called)
}

func TestOpenAIBPSTestConnectionUpstreamFailureStillReturns200(t *testing.T) {
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		account:          newOpenAIBPSTestAccount(),
	}
	router, handler := newOpenAIBPSTestRouter(accounts)
	handler.runTest = func(
		_ context.Context,
		_ *service.Account,
		model string,
		_ bool,
	) service.OpenAIBPSTestResult {
		return service.OpenAIBPSTestResult{
			Model:      model,
			HTTPStatus: http.StatusUnauthorized,
			ErrorCode:  "token_revoked",
			DurationMS: 55,
		}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		bytes.NewBufferString(`{"model":"gpt-6-astra"}`),
	))
	require.Equal(t, http.StatusOK, recorder.Code, "上游失败由 error_code 表达，HTTP 仍为 200")
	data := decodeOpenAIBPSTestResponse(t, recorder)
	require.Equal(t, http.StatusUnauthorized, data.HTTPStatus)
	require.Equal(t, "token_revoked", data.ErrorCode)
}

func TestOpenAIBPSTestConnectionMissingTokenUsesServiceValidation(t *testing.T) {
	// 真实 service 校验：BPS 账号缺 access_token 时不发网络请求，直接返回错误码。
	account := newOpenAIBPSTestAccount()
	delete(account.Credentials, "access_token")
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		account:          account,
	}
	router, _ := newOpenAIBPSTestRouter(accounts)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		bytes.NewBufferString(`{}`),
	))
	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeOpenAIBPSTestResponse(t, recorder)
	require.Equal(t, "bps_access_token_missing", data.ErrorCode)
	require.Zero(t, data.HTTPStatus)
}

func TestOpenAIBPSHandlerWithoutAccountSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewOpenAIBPSAccountHandler(nil)
	router := gin.New()
	RegisterOpenAIBPSAdminRoutes(router.Group("/admin"), handler)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/42/openai-bps/test-connection",
		bytes.NewBufferString(`{}`),
	))
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestNewOpenAIBPSAccountHandlerFromAccountHandler(t *testing.T) {
	accounts := &openAIBPSTestAdminService{
		stubAdminService: newStubAdminService(),
		account:          newOpenAIBPSTestAccount(),
	}
	base := &AccountHandler{adminService: accounts}
	handler := NewOpenAIBPSAccountHandlerFromAccountHandler(base)
	require.NotNil(t, handler)
	require.NotNil(t, handler.runTest)

	// nil base 也不能 panic（路由接线容错）。
	require.NotNil(t, NewOpenAIBPSAccountHandlerFromAccountHandler(nil))
}

func TestOpenAIBPSTestConnectionContextHasDeadline(t *testing.T) {
	// 生产 runner 使用 30s 超时的 http.Client；这里只验证构造出的 handler 带 runner，
	// 且超时常量为正，避免误配成 0（0 表示无限等待）。
	require.Positive(t, openAIBPSTestConnectionTimeout)
	require.Equal(t, 30*time.Second, openAIBPSTestConnectionTimeout)
	handler := NewOpenAIBPSAccountHandler(nil)
	require.NotNil(t, handler.runTest)
}

var _ openAIBPSAccountReader = (*openAIBPSTestAdminService)(nil)

// 保证接口可被 service.AdminService 满足（编译期检查）。
var _ openAIBPSAccountReader = (service.AdminService)(nil)
