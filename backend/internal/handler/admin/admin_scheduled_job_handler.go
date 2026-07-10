package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type AdminScheduledJobHandler struct {
	svc *service.AdminScheduledJobService
}

func NewAdminScheduledJobHandler(svc *service.AdminScheduledJobService) *AdminScheduledJobHandler {
	return &AdminScheduledJobHandler{svc: svc}
}

type createAdminScheduledJobRequest struct {
	Name           string `json:"name" binding:"required"`
	JobType        string `json:"job_type" binding:"required"`
	CronExpression string `json:"cron_expression" binding:"required"`
	Enabled        *bool  `json:"enabled"`
	PayloadJSON    string `json:"payload_json"`
	RetentionLimit int    `json:"retention_limit"`
}

type updateAdminScheduledJobRequest struct {
	Name           *string `json:"name"`
	CronExpression *string `json:"cron_expression"`
	Enabled        *bool   `json:"enabled"`
	PayloadJSON    *string `json:"payload_json"`
	RetentionLimit *int    `json:"retention_limit"`
}

func (h *AdminScheduledJobHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, items)
}

func (h *AdminScheduledJobHandler) Get(c *gin.Context) {
	id, ok := parseAdminScheduledJobID(c)
	if !ok {
		return
	}
	item, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "job not found")
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *AdminScheduledJobHandler) Create(c *gin.Context) {
	var req createAdminScheduledJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Authorization required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	item, err := h.svc.Create(c.Request.Context(), service.AdminScheduledJobCreateParams{
		Name:           strings.TrimSpace(req.Name),
		JobType:        strings.TrimSpace(req.JobType),
		CronExpression: strings.TrimSpace(req.CronExpression),
		Enabled:        enabled,
		PayloadJSON:    req.PayloadJSON,
		RetentionLimit: req.RetentionLimit,
		CreatedBy:      subject.UserID,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *AdminScheduledJobHandler) Update(c *gin.Context) {
	id, ok := parseAdminScheduledJobID(c)
	if !ok {
		return
	}
	var req updateAdminScheduledJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	item, err := h.svc.Update(c.Request.Context(), id, service.AdminScheduledJobUpdateParams{
		Name:           req.Name,
		CronExpression: req.CronExpression,
		Enabled:        req.Enabled,
		PayloadJSON:    req.PayloadJSON,
		RetentionLimit: req.RetentionLimit,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *AdminScheduledJobHandler) Delete(c *gin.Context) {
	id, ok := parseAdminScheduledJobID(c)
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *AdminScheduledJobHandler) RunNow(c *gin.Context) {
	id, ok := parseAdminScheduledJobID(c)
	if !ok {
		return
	}
	subject, found := servermiddleware.GetAuthSubjectFromContext(c)
	if !found {
		response.Unauthorized(c, "Authorization required")
		return
	}
	run, err := h.svc.RunNow(c.Request.Context(), id, service.AdminScheduledJobRunRequest{
		TriggeredByUser: &subject.UserID,
		TriggerType:     service.AdminScheduledJobTriggerManual,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, run)
}

func (h *AdminScheduledJobHandler) ListRuns(c *gin.Context) {
	id, ok := parseAdminScheduledJobID(c)
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	items, err := h.svc.ListRuns(c.Request.Context(), id, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, items)
}

func parseAdminScheduledJobID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid job id")
		return 0, false
	}
	return id, true
}
