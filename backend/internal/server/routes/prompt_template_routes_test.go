//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 该测试只验证路由注册：路径、参数名、方法是否与既有 groups 路由冲突。
// gin 在注册阶段就会 panic（例如同一位置出现不同参数名），因此这里用真实
// engine 注册一遍是最便宜的回归保护。handler 内部的 service 传 nil，
// 因为本测试不发起真实调用。

func TestRegisterPromptTemplateRoutes_RegistersExpectedEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	adminGroup := engine.Group("/api/v1/admin")
	handlers := &handler.Handlers{
		Admin: &handler.AdminHandlers{
			PromptTemplate: admin.NewPromptTemplateHandler(nil),
		},
	}

	// 既有路由使用 :id，这里先注册一条同参数名的路由，确保新路由与之共存。
	adminGroup.GET("/groups/:id/subscriptions", func(c *gin.Context) { c.Status(http.StatusOK) })

	require.NotPanics(t, func() {
		registerPromptTemplateRoutes(adminGroup, handlers)
	})

	routes := make(map[string]struct{})
	for _, r := range engine.Routes() {
		routes[r.Method+" "+r.Path] = struct{}{}
	}

	expected := []string{
		"GET /api/v1/admin/prompt-templates",
		"POST /api/v1/admin/prompt-templates",
		"GET /api/v1/admin/prompt-templates/:id",
		"PUT /api/v1/admin/prompt-templates/:id",
		"POST /api/v1/admin/prompt-templates/:id/archive",
		"GET /api/v1/admin/prompt-templates/:id/events",
		"GET /api/v1/admin/prompt-templates/:id/draft",
		"PUT /api/v1/admin/prompt-templates/:id/draft",
		"GET /api/v1/admin/prompt-templates/:id/validation",
		"GET /api/v1/admin/prompt-templates/:id/versions",
		"POST /api/v1/admin/prompt-templates/:id/versions",
		"GET /api/v1/admin/prompt-versions/:id",
		"POST /api/v1/admin/prompt-versions/:id/preview",
		"GET /api/v1/admin/groups/:id/prompt-binding",
		"PUT /api/v1/admin/groups/:id/prompt-binding",
		"DELETE /api/v1/admin/groups/:id/prompt-binding",
		"GET /api/v1/admin/groups/:id/prompt-overrides",
		"GET /api/v1/admin/groups/:id/accounts/:accountId/prompt-override",
		"PUT /api/v1/admin/groups/:id/accounts/:accountId/prompt-override",
		"DELETE /api/v1/admin/groups/:id/accounts/:accountId/prompt-override",
	}
	for _, want := range expected {
		_, ok := routes[want]
		require.Truef(t, ok, "route %q is not registered", want)
	}
}

func TestPromptTemplateRoutes_RejectInvalidIDsBeforeServiceCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	adminGroup := engine.Group("/api/v1/admin")
	handlers := &handler.Handlers{
		Admin: &handler.AdminHandlers{
			PromptTemplate: admin.NewPromptTemplateHandler(nil),
		},
	}
	registerPromptTemplateRoutes(adminGroup, handlers)

	// 非法 ID 必须在进入 service 之前被拒绝，否则 nil service 会 panic。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/prompt-templates/not-a-number", nil)
	require.NotPanics(t, func() { engine.ServeHTTP(rec, req) })
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/prompt-templates/0", nil)
	require.NotPanics(t, func() { engine.ServeHTTP(rec, req) })
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
