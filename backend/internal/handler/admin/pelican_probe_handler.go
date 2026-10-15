package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// PelicanProbeHandler handles admin pelican-probe (group IQ monitor) management.
type PelicanProbeHandler struct {
	probeSvc *service.PelicanProbeService
}

// NewPelicanProbeHandler creates a new PelicanProbeHandler.
func NewPelicanProbeHandler(probeSvc *service.PelicanProbeService) *PelicanProbeHandler {
	return &PelicanProbeHandler{probeSvc: probeSvc}
}

type createPelicanProbePlanRequest struct {
	Name            string `json:"name"`
	GroupID         int64  `json:"group_id" binding:"required"`
	ModelID         string `json:"model_id" binding:"required"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key" binding:"required"`
	Prompt          string `json:"prompt"`
	TestType        string `json:"test_type"`
	ReasoningEffort string `json:"reasoning_effort"`
	JudgeModelID    string `json:"judge_model_id"`
	CronExpression  string `json:"cron_expression"`
	Enabled         *bool  `json:"enabled"`
	IsPublic        *bool  `json:"is_public"`
	MaxResults      int    `json:"max_results"`
}

type updatePelicanProbePlanRequest struct {
	Name            *string `json:"name"`
	ModelID         *string `json:"model_id"`
	BaseURL         *string `json:"base_url"`
	APIKey          string  `json:"api_key"`
	Prompt          *string `json:"prompt"`
	TestType        *string `json:"test_type"`
	ReasoningEffort *string `json:"reasoning_effort"`
	JudgeModelID    *string `json:"judge_model_id"`
	CronExpression  *string `json:"cron_expression"`
	Enabled         *bool   `json:"enabled"`
	IsPublic        *bool   `json:"is_public"`
	MaxResults      *int    `json:"max_results"`
}

type setPelicanProbeEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type reviewPelicanProbeResultRequest struct {
	Verdict string `json:"verdict" binding:"required"`
	Note    string `json:"note"`
}

// ListPlans GET /admin/pelican-probe-plans?group_id=
func (h *PelicanProbeHandler) ListPlans(c *gin.Context) {
	var groupID *int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "invalid group_id")
			return
		}
		groupID = &id
	}
	plans, err := h.probeSvc.ListPlans(c.Request.Context(), groupID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, plans)
}

// CreatePlan POST /admin/pelican-probe-plans
func (h *PelicanProbeHandler) CreatePlan(c *gin.Context) {
	var req createPelicanProbePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	in := service.CreatePlanInput{
		Name:            req.Name,
		GroupID:         req.GroupID,
		ModelID:         req.ModelID,
		BaseURL:         req.BaseURL,
		APIKey:          req.APIKey,
		Prompt:          req.Prompt,
		TestType:        req.TestType,
		ReasoningEffort: req.ReasoningEffort,
		JudgeModelID:    req.JudgeModelID,
		CronExpression:  req.CronExpression,
		Enabled:         true,
		MaxResults:      req.MaxResults,
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	if req.IsPublic != nil {
		in.IsPublic = *req.IsPublic
	}
	created, err := h.probeSvc.CreatePlan(c.Request.Context(), in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, created)
}

// UpdatePlan PUT /admin/pelican-probe-plans/:id
func (h *PelicanProbeHandler) UpdatePlan(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}
	var req updatePelicanProbePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	in := service.UpdatePlanInput{
		Name:            req.Name,
		ModelID:         req.ModelID,
		BaseURL:         req.BaseURL,
		APIKey:          req.APIKey,
		Prompt:          req.Prompt,
		TestType:        req.TestType,
		ReasoningEffort: req.ReasoningEffort,
		JudgeModelID:    req.JudgeModelID,
		CronExpression:  req.CronExpression,
		Enabled:         req.Enabled,
		IsPublic:        req.IsPublic,
		MaxResults:      req.MaxResults,
	}
	updated, err := h.probeSvc.UpdatePlan(c.Request.Context(), planID, in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, updated)
}

// DeletePlan DELETE /admin/pelican-probe-plans/:id
func (h *PelicanProbeHandler) DeletePlan(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}
	if err := h.probeSvc.DeletePlan(c.Request.Context(), planID); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// RunNow POST /admin/pelican-probe-plans/:id/run
//
// Asynchronous by contract: only the cheap pre-checks run inside the request,
// the probe itself is handed to a detached background goroutine that owns a
// 10 minute budget, and the endpoint answers 202 immediately with
// {"started":true,"plan_id":N}. Nothing may wait for the probe here: the
// frontend apiClient aborts its request after 30s, which used to cancel every
// manual run while the model was still drawing.
//
// Re-entrancy: if a run for the same plan is already in flight the trigger is
// refused with HTTP 409 and body
// {"code":409,...,"data":{"started":false,"plan_id":N,"reason":"already_running"}}
// so a client can branch on either the status code or the payload.
func (h *PelicanProbeHandler) RunNow(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}
	trigger, err := h.probeSvc.StartPlanRun(c.Request.Context(), planID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrPelicanProbePlanNotFound):
			response.NotFound(c, err.Error())
		case errors.Is(err, service.ErrPelicanProbeAlreadyRunning):
			c.JSON(http.StatusConflict, response.Response{
				Code:    http.StatusConflict,
				Message: err.Error(),
				Data: gin.H{
					"started": false,
					"plan_id": planID,
					"reason":  "already_running",
				},
			})
		default:
			response.InternalError(c, err.Error())
		}
		return
	}
	response.Accepted(c, trigger)
}

// ListResults GET /admin/pelican-probe-plans/:id/results?limit=
func (h *PelicanProbeHandler) ListResults(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}
	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}
	results, err := h.probeSvc.ListResults(c.Request.Context(), planID, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}

// ListRecent GET /admin/pelican-probe-results?group_id=&limit=
func (h *PelicanProbeHandler) ListRecent(c *gin.Context) {
	var groupID *int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "invalid group_id")
			return
		}
		groupID = &id
	}
	limit := 60
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}
	results, err := h.probeSvc.ListRecentResults(c.Request.Context(), groupID, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}

// Review POST /admin/pelican-probe-results/:id/review
func (h *PelicanProbeHandler) Review(c *gin.Context) {
	resultID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid result id")
		return
	}
	var req reviewPelicanProbeResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	reviewer := currentAdminName(c)
	updated, err := h.probeSvc.ReviewResult(c.Request.Context(), resultID, req.Verdict, req.Note, reviewer)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, updated)
}

// GetGlobalStatus GET /admin/pelican-probe-status
func (h *PelicanProbeHandler) GetGlobalStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"enabled": h.probeSvc.GetGlobalEnabled(c.Request.Context())})
}

// SetGlobalStatus PUT /admin/pelican-probe-status
func (h *PelicanProbeHandler) SetGlobalStatus(c *gin.Context) {
	var req setPelicanProbeEnabledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.probeSvc.SetGlobalEnabled(c.Request.Context(), req.Enabled); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": req.Enabled})
}

// ListPublicStatus GET /user/pelican-probe/status
// NOTE: registered on the user route (JWT user auth); returns only plans
// explicitly marked public and never includes secrets.
func (h *PelicanProbeHandler) ListPublicStatus(c *gin.Context) {
	status, err := h.probeSvc.ListPublicStatus(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, status)
}

// currentAdminName best-effort resolves the acting admin identity for audit.
func currentAdminName(c *gin.Context) string {
	if v, ok := c.Get("admin_username"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if v, ok := c.Get("username"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if v, ok := c.Get("admin_email"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}
