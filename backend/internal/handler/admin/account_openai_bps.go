package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// OpenAI BPS 管理端接口。
//
// 本文件只放 BPS 平台专属的管理端能力：连接测试。BPS 账号的创建 / 更新校验、
// 凭证状态计算都在 service 层；dto.Account 已自动携带 bps_credential_state，
// 因此管理端不需要额外的"刷新凭证状态"接口。

const (
	// openAIBPSTestConnectionTimeout 是管理端测试连接的上游超时。
	// 与 service 层默认超时一致；测试连接是同步请求，超时必须有界。
	openAIBPSTestConnectionTimeout = 30 * time.Second
	// openAIBPSTestConnectionPath 是账号级测试连接路由（挂在 /admin/accounts 下）。
	openAIBPSTestConnectionPath = "/:id/openai-bps/test-connection"
)

// openAIBPSAccountReader 是测试连接所需的最小账号读取接口。
// *service.AccountService 与 service.AdminService 都满足该接口。
type openAIBPSAccountReader interface {
	GetAccount(ctx context.Context, id int64) (*service.Account, error)
}

// openAIBPSConnectionRunner 执行一次 BPS 连接测试。
// 生产实现调用 service.RunOpenAIBPSTestConnection；单测替换为固定结果，
// 避免真的请求固定上游。
type openAIBPSConnectionRunner func(
	ctx context.Context,
	account *service.Account,
	model string,
	compact bool,
) service.OpenAIBPSTestResult

// OpenAIBPSAccountHandler 处理 OpenAI BPS 账号的管理端操作（当前只有连接测试）。
type OpenAIBPSAccountHandler struct {
	accounts openAIBPSAccountReader
	runTest  openAIBPSConnectionRunner
}

// NewOpenAIBPSAccountHandler 用账号读取接口构造 BPS 管理端 handler。
func NewOpenAIBPSAccountHandler(accounts openAIBPSAccountReader) *OpenAIBPSAccountHandler {
	h := &OpenAIBPSAccountHandler{
		accounts: accounts,
	}
	client := &http.Client{Timeout: openAIBPSTestConnectionTimeout}
	h.runTest = func(
		ctx context.Context,
		account *service.Account,
		model string,
		compact bool,
	) service.OpenAIBPSTestResult {
		return service.RunOpenAIBPSTestConnection(ctx, account, model, compact, client)
	}
	return h
}

// NewOpenAIBPSAccountHandlerFromAccountHandler 从既有 AccountHandler 派生 BPS handler。
// 供路由接线使用：AccountHandler 已持有 service.AdminService，无需额外依赖注入。
func NewOpenAIBPSAccountHandlerFromAccountHandler(base *AccountHandler) *OpenAIBPSAccountHandler {
	var accounts openAIBPSAccountReader
	if base != nil && base.adminService != nil {
		accounts = base.adminService
	}
	return NewOpenAIBPSAccountHandler(accounts)
}

// RegisterOpenAIBPSAdminRoutes 注册 BPS 管理端路由。
//
// admin 是 /admin 路由组（即 registerAccountRoutes 的入参）；函数内部自建
// /accounts 子组，因此接线只需一行：
//
//	RegisterOpenAIBPSAdminRoutes(admin, NewOpenAIBPSAccountHandlerFromAccountHandler(h.Admin.Account))
func RegisterOpenAIBPSAdminRoutes(admin *gin.RouterGroup, h *OpenAIBPSAccountHandler) {
	if admin == nil || h == nil {
		return
	}
	accounts := admin.Group("/accounts")
	accounts.POST(openAIBPSTestConnectionPath, h.TestConnection)
}

// openAIBPSTestConnectionRequest 是测试连接请求体。
// 两个字段都可省略：缺省模型取服务端默认模型，缺省 compact=false。
type openAIBPSTestConnectionRequest struct {
	Model   string `json:"model"`
	Compact bool   `json:"compact"`
}

// openAIBPSTestConnectionResponse 是测试连接响应体。
// 测试失败（上游 401/网络错误等）同样返回 HTTP 200，用 error_code 表达原因；
// 只有请求本身非法（账号 ID 非法、账号不是 BPS、请求体损坏）才返回 4xx。
type openAIBPSTestConnectionResponse struct {
	AccountID     int64  `json:"account_id"`
	Model         string `json:"model"`
	Compact       bool   `json:"compact"`
	HTTPStatus    int    `json:"http_status"`
	UpstreamModel string `json:"upstream_model"`
	RequestID     string `json:"request_id"`
	ErrorCode     string `json:"error_code"`
	DurationMS    int    `json:"duration_ms"`
}

// TestConnection 处理 POST /admin/accounts/:id/openai-bps/test-connection。
func (h *OpenAIBPSAccountHandler) TestConnection(c *gin.Context) {
	accountID, ok := parseOpenAIBPSAccountID(c)
	if !ok {
		return
	}
	var req openAIBPSTestConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	account, err := h.loadAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account == nil || !account.IsOpenAIBPS() {
		response.BadRequest(c, "account is not an OpenAI BPS account")
		return
	}
	result := h.runTest(c.Request.Context(), account, strings.TrimSpace(req.Model), req.Compact)
	response.Success(c, openAIBPSTestConnectionResponse{
		AccountID:     accountID,
		Model:         result.Model,
		Compact:       result.Compact,
		HTTPStatus:    result.HTTPStatus,
		UpstreamModel: result.UpstreamModel,
		RequestID:     result.RequestID,
		ErrorCode:     result.ErrorCode,
		DurationMS:    result.DurationMS,
	})
}

// loadAccount 读取账号；handler 未注入账号来源时返回 500，避免 nil panic。
func (h *OpenAIBPSAccountHandler) loadAccount(ctx context.Context, accountID int64) (*service.Account, error) {
	if h.accounts == nil {
		return nil, service.ErrAccountNotFound
	}
	return h.accounts.GetAccount(ctx, accountID)
}

// parseOpenAIBPSAccountID 解析并校验路径中的账号 ID；失败时已写出 400 响应。
func parseOpenAIBPSAccountID(c *gin.Context) (int64, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return accountID, true
}
