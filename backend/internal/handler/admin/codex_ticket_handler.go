package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 本文件是 Codex 打票（票据）管理端：票据状态、手动打票、流水/事件/失效查询、
// 参与开关、全局开关/代理池/节奏设置。
//
// 路由注册函数与 handler 类型独立于既有 AccountHandler / ProxyHandler：
// Lead 只需在 handler.AdminHandlers 里加一个字段，并在 admin 路由组里调用
// RegisterCodexTicketRoutes(admin, h.CodexTicket)（见交付说明）。

// CodexTicketHandlerDeps 是 CodexTicketHandler 的构造依赖。
// Settings / Accounts 可为空（对应接口返回 CODEX_TICKET_UNAVAILABLE），便于渐进接线。
type CodexTicketHandlerDeps struct {
	Tickets  *service.CodexTicketService
	Settings *service.SettingService
	Accounts service.AccountRepository
}

// CodexTicketHandler 提供 Codex 票据中心与管理接口。
type CodexTicketHandler struct {
	tickets  *service.CodexTicketService
	settings *service.SettingService
	accounts service.AccountRepository
}

// NewCodexTicketHandler 构造 Codex 票据管理 handler。
func NewCodexTicketHandler(deps CodexTicketHandlerDeps) *CodexTicketHandler {
	return &CodexTicketHandler{tickets: deps.Tickets, settings: deps.Settings, accounts: deps.Accounts}
}

// RegisterCodexTicketRoutes 在给定 admin 路由组上注册票据相关路由。
// 路径与既有账号路由保持同一层级（/admin/accounts/:id/...），便于前端复用鉴权与审计中间件。
func RegisterCodexTicketRoutes(rg *gin.RouterGroup, h *CodexTicketHandler) {
	if rg == nil || h == nil {
		return
	}
	rg.GET("/accounts/:id/codex-tickets", h.GetAccountTicketStatuses)
	rg.GET("/accounts/:id/codex-ticket-history", h.GetTicketHistory)
	rg.GET("/accounts/:id/codex-ticket-events", h.GetTicketEvents)
	rg.POST("/accounts/:id/codex-ticket-harvest", h.HarvestTicket)
	rg.PUT("/accounts/:id/codex-ticket-participation", h.SetTicketParticipation)
	rg.GET("/accounts/:id/codex-ticket-invalidations", h.ListTicketInvalidations)
	rg.GET("/accounts/:id/codex-ticket-invalidations/:event_id", h.GetTicketInvalidation)
	rg.GET("/codex-tickets/settings", h.GetSettings)
	rg.PUT("/codex-tickets/enabled", h.SetEnabled)
	rg.PUT("/codex-tickets/allow-without-ticket", h.SetAllowWithoutTicket)
	rg.GET("/codex-tickets/pool", h.GetProxyPool)
	rg.PUT("/codex-tickets/pool", h.SetProxyPool)
	rg.GET("/codex-tickets/cadence", h.GetCadence)
	rg.PUT("/codex-tickets/cadence", h.SetCadence)
}

// codexTicketAccountID 解析路径里的账号 ID。
func codexTicketAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}

// codexTicketError 把服务层错误映射为管理端错误码。
func codexTicketError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCodexTicketBusy):
		response.ErrorWithDetails(c, http.StatusConflict, "Ticket attempt already running", "CODEX_TICKET_BUSY", nil)
	case errors.Is(err, service.ErrCodexTicketNoProxy):
		response.ErrorWithDetails(c, http.StatusUnprocessableEntity, "No available ticket proxy", "CODEX_TICKET_NO_PROXY", nil)
	case errors.Is(err, service.ErrCodexTicketModel):
		response.ErrorWithDetails(c, http.StatusBadRequest, "Unsupported ticket model", "CODEX_TICKET_MODEL", nil)
	case errors.Is(err, service.ErrCodexTicketRateLimited):
		response.ErrorWithDetails(c, http.StatusConflict, "Account or model is rate limited", "CODEX_TICKET_RATE_LIMITED", nil)
	case errors.Is(err, service.ErrCodexTicketUnavailable):
		response.ErrorWithDetails(c, http.StatusUnprocessableEntity, "Ticket harvesting disabled or account ineligible", "CODEX_TICKET_UNAVAILABLE", nil)
	case errors.Is(err, sql.ErrNoRows):
		response.NotFound(c, "Codex ticket event not found")
	default:
		if !response.ErrorFrom(c, err) {
			response.InternalError(c, "Codex ticket operation failed")
		}
	}
}

// requireTickets 检查票据服务是否已接线。
func (h *CodexTicketHandler) requireTickets(c *gin.Context) bool {
	if h == nil || h.tickets == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return false
	}
	return true
}

// requireSettings 检查设置服务是否已接线。
func (h *CodexTicketHandler) requireSettings(c *gin.Context) bool {
	if h == nil || h.settings == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return false
	}
	return true
}

// GetAccountTicketStatuses 返回账号在全部打票模型上的票据摘要 + 手动打票可用性。
// 注意：响应里不含票据本体。
func (h *CodexTicketHandler) GetAccountTicketStatuses(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	if h.accounts == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	account, err := h.accounts.GetByID(c.Request.Context(), id)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	// 先读 Enabled（必要时刷新设置快照），再计算逐模型状态。
	enabled := h.tickets.Enabled(c.Request.Context())
	statuses := h.tickets.Statuses(account)
	available, reason := true, ""
	if !enabled {
		available, reason = false, "disabled"
	} else if h.settings != nil {
		proxies, proxyErr := h.settings.AvailableCodexTicketProxies(c.Request.Context())
		if proxyErr != nil || len(proxies) == 0 {
			available, reason = false, "no_proxy"
		}
	}
	response.Success(c, gin.H{
		"account_id":                id,
		"items":                     statuses,
		"manual_available":          available,
		"manual_unavailable_reason": reason,
	})
}

// GetTicketHistory 返回账号打票流水（filter=all|success）。
func (h *CodexTicketHandler) GetTicketHistory(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	filter := c.DefaultQuery("filter", "all")
	if filter != "all" && filter != "success" {
		response.BadRequest(c, "filter must be all or success")
		return
	}
	page, size := response.ParsePagination(c)
	model := c.Query("model")
	items, total, err := h.tickets.HistoryFiltered(c.Request.Context(), id, model, filter == "success", page, size)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	var status service.CodexTicketStatus
	if model != "" && h.accounts != nil {
		if account, accErr := h.accounts.GetByID(c.Request.Context(), id); accErr == nil {
			status = h.tickets.TicketStatus(account, model)
		}
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size, "ticket_status": status})
}

// GetTicketEvents 返回 attempt + invalidation 合并时间线。
func (h *CodexTicketHandler) GetTicketEvents(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	filter := c.DefaultQuery("filter", "all")
	switch filter {
	case "all", "attempts", "success", "failure", "invalidation":
	default:
		response.BadRequest(c, "Invalid event filter")
		return
	}
	start, end, ok := parseCodexTicketTimeRange(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.tickets.Events(c.Request.Context(), id, c.Query("model"), filter, start, end, page, size)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// HarvestTicket 手动打一发票。
func (h *CodexTicketHandler) HarvestTicket(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	var input struct {
		Model string `json:"model" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "model is required")
		return
	}
	result, err := h.tickets.ManualHarvest(c.Request.Context(), id, input.Model)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, result)
}

// SetTicketParticipation 保存账号级打票参与开关与逐模型开关。
func (h *CodexTicketHandler) SetTicketParticipation(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	var input struct {
		Enabled *bool           `json:"enabled" binding:"required"`
		Models  map[string]bool `json:"models" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "enabled and models are required")
		return
	}
	if err := h.tickets.SetParticipation(c.Request.Context(), id, *input.Enabled, input.Models); err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": *input.Enabled, "models": input.Models})
}

// ListTicketInvalidations 返回票据失效流水列表。
func (h *CodexTicketHandler) ListTicketInvalidations(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	start, end, ok := parseCodexTicketTimeRange(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.tickets.ListInvalidations(c.Request.Context(), id, c.Query("model"), start, end, page, size)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// GetTicketInvalidation 返回单条失效流水详情。
func (h *CodexTicketHandler) GetTicketInvalidation(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store, max-age=0")
	c.Header("Pragma", "no-cache")
	id, ok := codexTicketAccountID(c)
	if !ok || !h.requireTickets(c) {
		return
	}
	eventID, err := strconv.ParseInt(c.Param("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		response.BadRequest(c, "Invalid event ID")
		return
	}
	item, err := h.tickets.GetInvalidation(c.Request.Context(), id, eventID)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"event_id": eventID})
	response.Success(c, item)
}

// GetSettings 返回打票全局设置（开关/放行/节奏/代理池/可用代理）。
func (h *CodexTicketHandler) GetSettings(c *gin.Context) {
	if !h.requireTickets(c) || !h.requireSettings(c) {
		return
	}
	ctx := c.Request.Context()
	pool, err := h.settings.GetCodexTicketPool(ctx)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	proxies, err := h.settings.AvailableCodexTicketProxies(ctx)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{
		"enabled":              h.tickets.Enabled(ctx),
		"allow_without_ticket": h.tickets.AllowWithoutTicket(ctx),
		"cadence":              h.tickets.Cadence(ctx),
		"pool":                 pool,
		"available_proxies":    proxies,
	})
}

// SetEnabled 设置全局打票开关。
func (h *CodexTicketHandler) SetEnabled(c *gin.Context) {
	if !h.requireTickets(c) {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "enabled is required")
		return
	}
	if err := h.tickets.SetEnabled(c.Request.Context(), *input.Enabled); err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": *input.Enabled})
}

// SetAllowWithoutTicket 设置「无票是否放行」。
func (h *CodexTicketHandler) SetAllowWithoutTicket(c *gin.Context) {
	if !h.requireTickets(c) {
		return
	}
	var input struct {
		Allow *bool `json:"allow" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "allow is required")
		return
	}
	if err := h.tickets.SetAllowWithoutTicket(c.Request.Context(), *input.Allow); err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"allow_without_ticket": *input.Allow})
}

// GetProxyPool 返回打票代理池设置。
func (h *CodexTicketHandler) GetProxyPool(c *gin.Context) {
	if !h.requireSettings(c) {
		return
	}
	pool, err := h.settings.GetCodexTicketPool(c.Request.Context())
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, pool)
}

// SetProxyPool 保存打票代理池设置。
func (h *CodexTicketHandler) SetProxyPool(c *gin.Context) {
	if !h.requireSettings(c) {
		return
	}
	var pool service.CodexTicketProxyPool
	if err := c.ShouldBindJSON(&pool); err != nil {
		response.BadRequest(c, "Invalid proxy pool")
		return
	}
	if h.tickets != nil {
		if err := h.tickets.SetProxyPool(c.Request.Context(), pool); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	} else if err := h.settings.SetCodexTicketPool(c.Request.Context(), pool); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	saved, err := h.settings.GetCodexTicketPool(c.Request.Context())
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, saved)
}

// GetCadence 返回打票节奏。
func (h *CodexTicketHandler) GetCadence(c *gin.Context) {
	if !h.requireTickets(c) {
		return
	}
	response.Success(c, h.tickets.Cadence(c.Request.Context()))
}

// SetCadence 保存打票节奏。
func (h *CodexTicketHandler) SetCadence(c *gin.Context) {
	if !h.requireSettings(c) {
		return
	}
	var cadence service.CodexTicketCadence
	if err := c.ShouldBindJSON(&cadence); err != nil {
		response.BadRequest(c, "Invalid cadence")
		return
	}
	if err := h.settings.SetCodexTicketCadence(c.Request.Context(), cadence); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if h.tickets != nil {
		_ = h.tickets.SetCadence(c.Request.Context(), cadence)
	}
	response.Success(c, cadence)
}

// parseCodexTicketTimeRange 解析 start_time/end_time（RFC3339），默认最近 90 天。
func parseCodexTicketTimeRange(c *gin.Context) (time.Time, time.Time, bool) {
	end := time.Now().UTC().Add(time.Second)
	start := end.Add(-90 * 24 * time.Hour)
	if value := c.Query("start_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid start_time")
			return time.Time{}, time.Time{}, false
		}
		if parsed.After(start) {
			start = parsed
		}
	}
	if value := c.Query("end_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid end_time")
			return time.Time{}, time.Time{}, false
		}
		if parsed.Before(end) {
			end = parsed
		}
	}
	if !end.After(start) {
		response.BadRequest(c, "Invalid time range")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}
