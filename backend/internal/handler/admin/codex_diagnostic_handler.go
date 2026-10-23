package admin

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodexDiagnosticHandler 暴露 Codex 降智检测（ModelTrace 指纹）的管理面接口：
// 账号原生探测、设置、可编辑 JSONL 探测模板与历史结果。
type CodexDiagnosticHandler struct {
	svc *service.CodexDegradationService
}

// NewCodexDiagnosticHandler 构造管理面 handler。
func NewCodexDiagnosticHandler(svc *service.CodexDegradationService) *CodexDiagnosticHandler {
	return &CodexDiagnosticHandler{svc: svc}
}

type codexDiagnoseRequest struct {
	Models []string `json:"models"`
}

type codexDegradationSettingsRequest struct {
	Enabled         *bool    `json:"enabled"`
	IntervalMinutes *int     `json:"interval_minutes"`
	BlockMinutes    *int     `json:"block_minutes"`
	Models          []string `json:"models"`
}

type codexProbeTemplateRequest struct {
	Template string `json:"template"`
}

// RegisterCodexDiagnosticRoutes 在给定路由组下注册降智检测路由。
// Lead 接线：RegisterCodexDiagnosticRoutes(admin, h.Admin.CodexDiagnostic)。
func RegisterCodexDiagnosticRoutes(rg *gin.RouterGroup, h *CodexDiagnosticHandler) {
	if rg == nil || h == nil {
		return
	}
	group := rg.Group("/codex-degradation")
	{
		group.GET("/models", h.ListModels)
		group.GET("/settings", h.GetSettings)
		group.PUT("/settings", h.SaveSettings)
		group.GET("/probe-template", h.GetProbeTemplate)
		group.PUT("/probe-template", h.SaveProbeTemplate)
		group.POST("/run", h.RunNow)
		group.POST("/accounts/:id/diagnose", h.DiagnoseAccount)
		group.GET("/accounts/:id/results", h.ListResults)
	}
}

// ListModels GET /codex-degradation/models
// 返回可检测模型与当前内嵌指纹库提交号。
func (h *CodexDiagnosticHandler) ListModels(c *gin.Context) {
	models, err := h.svc.DiagnosticModels()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, gin.H{
		"models": models,
		"bank":   service.ModelTraceBankCommit(),
	})
}

// GetSettings GET /codex-degradation/settings
func (h *CodexDiagnosticHandler) GetSettings(c *gin.Context) {
	settings, err := h.svc.GetSettings(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, settings)
}

// SaveSettings PUT /codex-degradation/settings
func (h *CodexDiagnosticHandler) SaveSettings(c *gin.Context) {
	var req codexDegradationSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	current, err := h.svc.GetSettings(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	next := *current
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if req.IntervalMinutes != nil {
		next.IntervalMinutes = *req.IntervalMinutes
	}
	if req.BlockMinutes != nil {
		next.BlockMinutes = *req.BlockMinutes
	}
	if req.Models != nil {
		next.Models = req.Models
	}
	if err := h.svc.SaveSettings(c.Request.Context(), &next); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, next)
}

// GetProbeTemplate GET /codex-degradation/probe-template
func (h *CodexDiagnosticHandler) GetProbeTemplate(c *gin.Context) {
	raw, err := h.svc.GetProbeTemplateRaw(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, gin.H{
		"template":         raw,
		"default_template": service.DefaultCodexProbeTemplate(),
	})
}

// SaveProbeTemplate PUT /codex-degradation/probe-template
// 模板非法时拒绝保存；探测请求同样会在发请求前拒绝非法模板。
func (h *CodexDiagnosticHandler) SaveProbeTemplate(c *gin.Context) {
	var req codexProbeTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.svc.SaveProbeTemplate(c.Request.Context(), req.Template); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"saved": true})
}

// RunNow POST /codex-degradation/run
// 立即触发一轮自动检测（设置关闭时不会产生探测）。
func (h *CodexDiagnosticHandler) RunNow(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Minute)
	defer cancel()
	if err := h.svc.RunDue(ctx); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, gin.H{"triggered": true})
}

// DiagnoseAccount POST /codex-degradation/accounts/:id/diagnose
// 用目标账号自身的上游凭据逐个模型打探测，不经过公开网关、不消耗用户 API Key。
func (h *CodexDiagnosticHandler) DiagnoseAccount(c *gin.Context) {
	accountID, ok := parseCodexDiagnosticAccountID(c)
	if !ok {
		return
	}
	var req codexDiagnoseRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, err.Error())
		return
	}
	if len(req.Models) > 32 {
		response.BadRequest(c, "Select up to 32 models")
		return
	}
	result, err := h.svc.CheckAccount(c.Request.Context(), accountID, req.Models)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListResults GET /codex-degradation/accounts/:id/results
func (h *CodexDiagnosticHandler) ListResults(c *gin.Context) {
	accountID, ok := parseCodexDiagnosticAccountID(c)
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	records, err := h.svc.ListProbeResults(c.Request.Context(), accountID, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, gin.H{"items": records})
}

func parseCodexDiagnosticAccountID(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	accountID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return 0, false
	}
	return accountID, true
}
