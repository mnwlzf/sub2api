package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// PromptTemplateHandler 处理文本提示词模板、版本与绑定的管理请求。
//
// 注意路由前缀使用 /admin/prompt-templates 与 /admin/prompt-versions：
// /admin/prompt-audit 已被既有的“提示词审计”功能占用，不要复用。
type PromptTemplateHandler struct {
	service *service.PromptTemplateService
}

// NewPromptTemplateHandler 创建文本提示词管理处理器。
func NewPromptTemplateHandler(service *service.PromptTemplateService) *PromptTemplateHandler {
	return &PromptTemplateHandler{service: service}
}

// promptActorFrom 从 Gin context 提取操作者，用于管理审计。
func promptActorFrom(c *gin.Context) service.PromptActor {
	actor := service.PromptActor{RequestID: c.GetString("request_id")}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		id := subject.UserID
		actor.ID = &id
	}
	actor.Name = c.GetString(string(middleware.ContextKeyAuthEmail))
	return actor
}

// parsePromptID 解析路径参数中的整数 ID。
func parsePromptID(c *gin.Context, name string) (int64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid "+name)
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------- 模板

// CreatePromptTemplateRequest 创建模板。
type CreatePromptTemplateRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
	SourceNote  string `json:"source_note"`
}

// UpdatePromptTemplateRequest 更新模板元数据（必须携带 revision）。
type UpdatePromptTemplateRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
	SourceNote  string `json:"source_note"`
	Revision    int    `json:"revision" binding:"required"`
}

// ListTemplates 列出模板。
// GET /api/v1/admin/prompt-templates?include_archived=true
func (h *PromptTemplateHandler) ListTemplates(c *gin.Context) {
	includeArchived := strings.EqualFold(c.Query("include_archived"), "true")
	items, err := h.service.ListTemplates(c.Request.Context(), includeArchived)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// CreateTemplate 创建模板。
// POST /api/v1/admin/prompt-templates
func (h *PromptTemplateHandler) CreateTemplate(c *gin.Context) {
	var req CreatePromptTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	created, err := h.service.CreateTemplate(c.Request.Context(), service.PromptTemplateInput{
		Name:        req.Name,
		Description: req.Description,
		SourceURL:   req.SourceURL,
		SourceNote:  req.SourceNote,
	}, promptActorFrom(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, created)
}

// GetTemplate 读取模板详情。
// GET /api/v1/admin/prompt-templates/:id
func (h *PromptTemplateHandler) GetTemplate(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	template, err := h.service.GetTemplate(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, template)
}

// UpdateTemplate 更新模板元数据。
// PUT /api/v1/admin/prompt-templates/:id
func (h *PromptTemplateHandler) UpdateTemplate(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	var req UpdatePromptTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	updated, err := h.service.UpdateTemplate(c.Request.Context(), id, req.Revision, service.PromptTemplateInput{
		Name:        req.Name,
		Description: req.Description,
		SourceURL:   req.SourceURL,
		SourceNote:  req.SourceNote,
	}, promptActorFrom(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// ArchiveTemplateRequest 归档模板。
type ArchiveTemplateRequest struct {
	Revision int `json:"revision" binding:"required"`
}

// ArchiveTemplate 归档模板。既有绑定继续运行，但不能新增绑定。
// POST /api/v1/admin/prompt-templates/:id/archive
func (h *PromptTemplateHandler) ArchiveTemplate(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	var req ArchiveTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.ArchiveTemplate(c.Request.Context(), id, req.Revision, promptActorFrom(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"archived": true, "id": id})
}

// ListEvents 查询管理审计事件（不含正文）。
// GET /api/v1/admin/prompt-templates/:id/events
func (h *PromptTemplateHandler) ListEvents(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	events, err := h.service.ListAdminEvents(c.Request.Context(), &id, nil, 100)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, events)
}

// ---------------------------------------------------------------- 草稿

// PromptDraftRequest 更新草稿。
type PromptDraftRequest struct {
	Body              string   `json:"body" binding:"required"`
	ClientModels      []string `json:"client_models"`
	UpstreamModels    []string `json:"upstream_models"`
	SupportedProfiles []string `json:"supported_profiles"`
	// Revision 为 0 表示首次写入；非 0 时必须与当前草稿 revision 一致。
	Revision int `json:"revision"`
}

// GetDraft 读取草稿。
// GET /api/v1/admin/prompt-templates/:id/draft
func (h *PromptTemplateHandler) GetDraft(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	draft, err := h.service.GetDraft(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, draft)
}

// UpdateDraft 更新草稿。
// PUT /api/v1/admin/prompt-templates/:id/draft
func (h *PromptTemplateHandler) UpdateDraft(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	var req PromptDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	draft, err := h.service.UpdateDraft(c.Request.Context(), id, req.Revision, service.PromptDraftInput{
		Body:              req.Body,
		ClientModels:      req.ClientModels,
		UpstreamModels:    req.UpstreamModels,
		SupportedProfiles: req.SupportedProfiles,
	}, promptActorFrom(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, draft)
}

// ValidateDraft 返回发布前校验结果（不写库、不调用上游）。
// GET /api/v1/admin/prompt-templates/:id/validation
func (h *PromptTemplateHandler) ValidateDraft(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	report, err := h.service.ValidateDraft(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, report)
}

// ---------------------------------------------------------------- 版本

// PublishVersionRequest 发布草稿为不可变版本。
type PublishVersionRequest struct {
	DraftRevision  int    `json:"draft_revision" binding:"required"`
	ChangeNote     string `json:"change_note"`
	IdempotencyKey string `json:"idempotency_key"`
}

// PublishVersion 发布草稿。重放同一个 idempotency_key 返回已存在的版本。
// POST /api/v1/admin/prompt-templates/:id/versions
func (h *PromptTemplateHandler) PublishVersion(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	var req PublishVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	version, err := h.service.PublishVersion(c.Request.Context(), id, req.DraftRevision, req.ChangeNote, req.IdempotencyKey, promptActorFrom(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, version)
}

// ListVersions 列出模板的已发布版本。
// GET /api/v1/admin/prompt-templates/:id/versions
func (h *PromptTemplateHandler) ListVersions(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	versions, err := h.service.ListVersions(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, versions)
}

// GetVersion 读取固定版本快照。
// GET /api/v1/admin/prompt-versions/:id
func (h *PromptTemplateHandler) GetVersion(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	version, err := h.service.GetVersion(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, version)
}

// PreviewVersion 返回无上游调用的结构预览。
// POST /api/v1/admin/prompt-versions/:id/preview
func (h *PromptTemplateHandler) PreviewVersion(c *gin.Context) {
	id, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	preview, err := h.service.PreviewVersion(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

// ---------------------------------------------------------------- 分组绑定

// PromptBindingRequest 设置分组绑定。
type PromptBindingRequest struct {
	Mode      string `json:"mode" binding:"required"`
	VersionID *int64 `json:"version_id"`
	Revision  int    `json:"revision"`
}

// GetGroupBinding 读取分组绑定。没有绑定行时返回 disabled 语义。
// GET /api/v1/admin/groups/:id/prompt-binding
func (h *PromptTemplateHandler) GetGroupBinding(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	binding, err := h.service.GetGroupBinding(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, binding)
}

// SetGroupBinding 设置分组绑定。
// PUT /api/v1/admin/groups/:id/prompt-binding
func (h *PromptTemplateHandler) SetGroupBinding(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	var req PromptBindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	binding, err := h.service.SetGroupBinding(c.Request.Context(), groupID, req.Revision, service.PromptBindingInput{
		Mode:      req.Mode,
		VersionID: req.VersionID,
	}, promptActorFrom(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, binding)
}

// ClearGroupBinding 删除绑定行，等价于关闭该分组。
// DELETE /api/v1/admin/groups/:id/prompt-binding
func (h *PromptTemplateHandler) ClearGroupBinding(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	if err := h.service.ClearGroupBinding(c.Request.Context(), groupID, promptActorFrom(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"group_id": groupID, "mode": service.PromptBindingModeDisabled})
}

// ---------------------------------------------------------------- 账号覆盖

// PromptOverrideRequest 设置账号在该分组内的覆盖。
type PromptOverrideRequest struct {
	Mode      string `json:"mode" binding:"required"`
	VersionID *int64 `json:"version_id"`
	Revision  int    `json:"revision"`
}

// ListAccountOverrides 列出分组内的账号覆盖。
// GET /api/v1/admin/groups/:id/prompt-overrides
func (h *PromptTemplateHandler) ListAccountOverrides(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	overrides, err := h.service.ListAccountOverrides(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, overrides)
}

// GetAccountOverride 读取账号在分组内的覆盖。
// GET /api/v1/admin/groups/:id/accounts/:accountId/prompt-override
func (h *PromptTemplateHandler) GetAccountOverride(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	accountID, ok := parsePromptID(c, "accountId")
	if !ok {
		return
	}
	override, err := h.service.GetAccountOverride(c.Request.Context(), accountID, groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, override)
}

// SetAccountOverride 设置账号在分组内的覆盖。
// PUT /api/v1/admin/groups/:id/accounts/:accountId/prompt-override
func (h *PromptTemplateHandler) SetAccountOverride(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	accountID, ok := parsePromptID(c, "accountId")
	if !ok {
		return
	}
	var req PromptOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	override, err := h.service.SetAccountOverride(c.Request.Context(), accountID, groupID, req.Revision, service.PromptOverrideInput{
		Mode:      req.Mode,
		VersionID: req.VersionID,
	}, promptActorFrom(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, override)
}

// ClearAccountOverride 删除覆盖行，回到继承分组。
// DELETE /api/v1/admin/groups/:id/accounts/:accountId/prompt-override
func (h *PromptTemplateHandler) ClearAccountOverride(c *gin.Context) {
	groupID, ok := parsePromptID(c, "id")
	if !ok {
		return
	}
	accountID, ok := parsePromptID(c, "accountId")
	if !ok {
		return
	}
	if err := h.service.ClearAccountOverride(c.Request.Context(), accountID, groupID, promptActorFrom(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": accountID, "group_id": groupID, "mode": service.PromptOverrideModeInherit})
}
