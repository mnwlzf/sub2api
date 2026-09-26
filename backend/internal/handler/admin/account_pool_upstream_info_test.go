package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupPoolUpstreamInfoRouter(adminService service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetUpstreamBillingProbeService(service.NewUpstreamBillingProbeService(nil, nil, nil))

	router := gin.New()
	router.GET("/admin/accounts/upstream-billing-rates", handler.GetUpstreamBillingRates)
	router.POST("/admin/accounts/:id/pool-upstream-info-probe", handler.ProbePoolUpstreamInfo)
	return router
}

func TestAccountHandlerProbePoolUpstreamInfoRejectsInvalidID(t *testing.T) {
	router := setupPoolUpstreamInfoRouter(nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/accounts/not-an-id/pool-upstream-info-probe", nil))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestAccountHandlerProbePoolUpstreamInfoUnavailableWithoutRepository(t *testing.T) {
	router := setupPoolUpstreamInfoRouter(nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/accounts/1/pool-upstream-info-probe", nil))

	// The probe service has no account repository: a service-level error must
	// surface rather than a panic or a fake success.
	require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
	require.NotEqual(t, http.StatusOK, recorder.Code)
}

func TestAccountHandlerProbePoolUpstreamInfoUnavailableWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/admin/accounts/:id/pool-upstream-info-probe", handler.ProbePoolUpstreamInfo)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/accounts/1/pool-upstream-info-probe", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestGetUpstreamBillingRatesIncludesPoolUpstreamInfoAndETag(t *testing.T) {
	admin := newStubAdminService()
	receivedAt := time.Date(2026, time.September, 27, 10, 0, 0, 0, time.UTC)
	freshUntil := receivedAt.Add(time.Hour)
	admin.accounts = []service.Account{
		{
			ID:       3,
			Name:     "account",
			Platform: service.PlatformAnthropic,
			Type:     service.AccountTypeAPIKey,
			Status:   service.StatusActive,
			Extra: map[string]any{
				service.PoolUpstreamPlatformExtraKey: service.PoolUpstreamPlatformSub2API,
				service.PoolUpstreamFeaturesExtraKey: []any{"balance"},
				service.PoolUpstreamInfoExtraKey: map[string]any{
					"status":          "ok",
					"platform":        "sub2api",
					"features":        []any{"balance"},
					"data":            map[string]any{"kind": "wallet", "amount_usd": 12.5},
					"received_at":     receivedAt.Format(time.RFC3339Nano),
					"fresh_until":     freshUntil.Format(time.RFC3339Nano),
					"last_attempt_at": receivedAt.Format(time.RFC3339Nano),
					"next_probe_at":   freshUntil.Format(time.RFC3339Nano),
					"http_status":     200,
				},
			},
		},
	}
	router := setupPoolUpstreamInfoRouter(admin)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-rates", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Data struct {
			Items []struct {
				AccountID        int64                          `json:"account_id"`
				PoolUpstreamInfo *service.PoolUpstreamInfoSnapshot `json:"pool_upstream_info"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Data.Items, 1)
	info := body.Data.Items[0].PoolUpstreamInfo
	require.NotNil(t, info)
	require.Equal(t, service.PoolUpstreamInfoStatusOK, info.Status)
	require.Equal(t, service.PoolUpstreamPlatformSub2API, info.Platform)
	require.Equal(t, "wallet", info.Data["kind"])
	require.Equal(t, 12.5, info.Data["amount_usd"])

	etag := recorder.Header().Get("ETag")
	require.NotEmpty(t, etag)

	revalidate := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-rates", nil)
	request.Header.Set("If-None-Match", etag)
	router.ServeHTTP(revalidate, request)
	require.Equal(t, http.StatusNotModified, revalidate.Code)
}
