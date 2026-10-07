package service

import (
	"context"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// PromptTemplateService 是文本提示词配置面的业务服务。
//
// 不变量：
//   - 已发布版本内容不可变（数据库 trigger 兜底）。
//   - 发布必须携带草稿 revision，避免两个管理员互相覆盖。
//   - 所有写操作与对应管理审计事件在同一事务内提交。
//   - 归档模板不能新增绑定，但既有绑定继续运行。
type PromptTemplateService struct {
	repo PromptTemplateRepository
}

// NewPromptTemplateService 构造配置面服务。
func NewPromptTemplateService(repo PromptTemplateRepository) *PromptTemplateService {
	return &PromptTemplateService{repo: repo}
}

// PromptActor 描述一次管理操作的执行者，用于审计。
type PromptActor struct {
	ID        *int64
	Name      string
	RequestID string
}

func (a PromptActor) event(action, scope string) *PromptAdminEvent {
	return &PromptAdminEvent{
		Action:    action,
		Scope:     scope,
		ActorID:   a.ID,
		ActorName: a.Name,
		RequestID: a.RequestID,
	}
}

// PromptTemplateInput 是模板元数据输入。
type PromptTemplateInput struct {
	Name        string
	Description string
	SourceURL   string
	SourceNote  string
}

// PromptDraftInput 是草稿输入。
type PromptDraftInput struct {
	Body              string
	ClientModels      []string
	UpstreamModels    []string
	SupportedProfiles []string
}

// PromptBindingInput 是分组绑定输入。
type PromptBindingInput struct {
	Mode      string
	VersionID *int64
}

// PromptOverrideInput 是账号覆盖输入。
type PromptOverrideInput struct {
	Mode      string
	VersionID *int64
}

// PromptPreviewResult 是无上游调用的结构预览结果。
type PromptPreviewResult struct {
	VersionID         int64    `json:"version_id"`
	ManifestSHA256    string   `json:"manifest_sha256"`
	BodyBytes         int      `json:"body_bytes"`
	AddedBytes        int      `json:"added_bytes"`
	ClientModels      []string `json:"client_models"`
	SupportedProfiles []string `json:"supported_profiles"`
	// ProfilePreviews 按 profile 展示注入后的关键字段与是否保留了客户端原有内容。
	ProfilePreviews []PromptProfilePreview `json:"profile_previews"`
}

// PromptProfilePreview 是单个 profile 的预览。
type PromptProfilePreview struct {
	Profile       string `json:"profile"`
	Supported     bool   `json:"supported"`
	Field         string `json:"field"`
	OriginalValue string `json:"original_value,omitempty"`
	InjectedValue string `json:"injected_value,omitempty"`
	Error         string `json:"error,omitempty"`
}

// ---------------------------------------------------------------- 模板

// ListTemplates 列出模板。
func (s *PromptTemplateService) ListTemplates(ctx context.Context, includeArchived bool) ([]PromptTemplate, error) {
	return s.repo.ListTemplates(ctx, includeArchived)
}

// GetTemplate 读取单个模板。
func (s *PromptTemplateService) GetTemplate(ctx context.Context, id int64) (*PromptTemplate, error) {
	return s.repo.GetTemplate(ctx, id)
}

// CreateTemplate 创建模板，并同时建立一份空草稿。
func (s *PromptTemplateService) CreateTemplate(ctx context.Context, input PromptTemplateInput, actor PromptActor) (*PromptTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, infraerrors.BadRequest("PROMPT_NAME_EMPTY", "template name must not be empty")
	}
	if len(name) > 100 {
		return nil, infraerrors.BadRequest("PROMPT_NAME_TOO_LONG", "template name must be at most 100 characters")
	}

	template := &PromptTemplate{
		Name:        name,
		Description: strings.TrimSpace(input.Description),
		SourceURL:   strings.TrimSpace(input.SourceURL),
		SourceNote:  strings.TrimSpace(input.SourceNote),
		Revision:    1,
		CreatedBy:   actor.ID,
		UpdatedBy:   actor.ID,
	}

	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.CreateTemplate(txCtx, template); err != nil {
			return err
		}
		// 新模板先给一份占位草稿：管理员随后编辑正文，未发布前不会影响任何请求。
		draft := &PromptTemplateDraft{
			TemplateID:        template.ID,
			Body:              "placeholder",
			ClientModels:      []string{},
			UpstreamModels:    []string{},
			SupportedProfiles: []string{},
			UpdatedBy:         actor.ID,
		}
		if err := s.repo.UpsertDraft(txCtx, draft, 0, true); err != nil {
			return err
		}
		event := actor.event(PromptActionCreate, PromptScopeTemplate)
		event.TemplateID = &template.ID
		event.AfterState = map[string]any{"name": template.Name}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
	if err != nil {
		return nil, err
	}
	return template, nil
}

// UpdateTemplate 更新模板元数据（乐观锁）。
func (s *PromptTemplateService) UpdateTemplate(ctx context.Context, id int64, expectedRevision int, input PromptTemplateInput, actor PromptActor) (*PromptTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, infraerrors.BadRequest("PROMPT_NAME_EMPTY", "template name must not be empty")
	}
	if expectedRevision <= 0 {
		return nil, infraerrors.BadRequest("PROMPT_REVISION_REQUIRED", "expected_revision is required")
	}

	var updated *PromptTemplate
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		current, err := s.repo.GetTemplate(txCtx, id)
		if err != nil {
			return err
		}
		if current.IsArchived() {
			return ErrPromptTemplateArchived
		}
		next := *current
		next.Name = name
		next.Description = strings.TrimSpace(input.Description)
		next.SourceURL = strings.TrimSpace(input.SourceURL)
		next.SourceNote = strings.TrimSpace(input.SourceNote)
		next.UpdatedBy = actor.ID
		if err := s.repo.UpdateTemplate(txCtx, &next, expectedRevision); err != nil {
			return err
		}
		updated = &next
		event := actor.event(PromptActionUpdate, PromptScopeTemplate)
		event.TemplateID = &id
		event.BeforeState = map[string]any{"name": current.Name}
		event.AfterState = map[string]any{"name": next.Name}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ArchiveTemplate 归档模板。既有绑定继续运行，但不能新增绑定。
func (s *PromptTemplateService) ArchiveTemplate(ctx context.Context, id int64, expectedRevision int, actor PromptActor) error {
	if expectedRevision <= 0 {
		return infraerrors.BadRequest("PROMPT_REVISION_REQUIRED", "expected_revision is required")
	}
	return s.repo.WithTx(ctx, func(txCtx context.Context) error {
		current, err := s.repo.GetTemplate(txCtx, id)
		if err != nil {
			return err
		}
		if current.IsArchived() {
			return ErrPromptTemplateArchived
		}
		if err := s.repo.ArchiveTemplate(txCtx, id, expectedRevision, actor.ID); err != nil {
			return err
		}
		event := actor.event(PromptActionArchive, PromptScopeTemplate)
		event.TemplateID = &id
		event.BeforeState = map[string]any{"archived": false}
		event.AfterState = map[string]any{"archived": true}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
}

// ---------------------------------------------------------------- 草稿

// GetDraft 读取草稿。
func (s *PromptTemplateService) GetDraft(ctx context.Context, templateID int64) (*PromptTemplateDraft, error) {
	return s.repo.GetDraft(ctx, templateID)
}

// UpdateDraft 更新草稿（乐观锁）。
func (s *PromptTemplateService) UpdateDraft(ctx context.Context, templateID int64, expectedRevision int, input PromptDraftInput, actor PromptActor) (*PromptTemplateDraft, error) {
	if err := ValidatePromptBody(input.Body); err != nil {
		return nil, err
	}
	if err := ValidatePromptProfiles(input.SupportedProfiles); err != nil {
		return nil, err
	}

	var saved *PromptTemplateDraft
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		template, err := s.repo.GetTemplate(txCtx, templateID)
		if err != nil {
			return err
		}
		if template.IsArchived() {
			return ErrPromptTemplateArchived
		}

		draft := &PromptTemplateDraft{
			TemplateID:        templateID,
			Body:              input.Body,
			ClientModels:      PromptNormalizeSet(input.ClientModels),
			UpstreamModels:    PromptNormalizeSet(input.UpstreamModels),
			SupportedProfiles: PromptNormalizeSet(input.SupportedProfiles),
			UpdatedBy:         actor.ID,
		}

		current, getErr := s.repo.GetDraft(txCtx, templateID)
		switch {
		case getErr == nil && current != nil:
			// 行已存在时必须匹配 revision。expectedRevision<=0 意味着调用方以为
			// “还没有配置”（例如页面加载时读到空），属于过期副本 —— 此时另一个
			// 管理员可能刚创建或改过，必须报冲突而不是静默覆盖对方。
			if current.Revision != expectedRevision {
				return ErrPromptRevisionConflict
			}
			if err := s.repo.UpsertDraft(txCtx, draft, current.Revision, false); err != nil {
				return err
			}
		case errors.Is(getErr, ErrPromptDraftNotFound):
			if err := s.repo.UpsertDraft(txCtx, draft, 0, true); err != nil {
				return err
			}
		default:
			return getErr
		}
		saved = draft

		event := actor.event(PromptActionUpdateDraft, PromptScopeTemplate)
		event.TemplateID = &templateID
		// 审计只记录结构信息与正文摘要，不记录正文本身。
		event.AfterState = map[string]any{
			"body_sha256":        PromptBodySHA256(draft.Body),
			"body_bytes":         len(draft.Body),
			"client_models":      draft.ClientModels,
			"supported_profiles": draft.SupportedProfiles,
		}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// ---------------------------------------------------------------- 版本

// ListVersions 列出模板的已发布版本（新到旧）。
func (s *PromptTemplateService) ListVersions(ctx context.Context, templateID int64) ([]PromptTemplateVersion, error) {
	return s.repo.ListVersions(ctx, templateID)
}

// GetVersion 读取指定版本。
func (s *PromptTemplateService) GetVersion(ctx context.Context, id int64) (*PromptTemplateVersion, error) {
	return s.repo.GetVersion(ctx, id)
}

// PublishVersion 把当前草稿发布成不可变版本。
//
// idempotencyKey 非空时，重放同一个键会返回已存在的版本而不是再发一版。
func (s *PromptTemplateService) PublishVersion(ctx context.Context, templateID int64, draftRevision int, changeNote, idempotencyKey string, actor PromptActor) (*PromptTemplateVersion, error) {
	if draftRevision <= 0 {
		return nil, infraerrors.BadRequest("PROMPT_DRAFT_REVISION_REQUIRED", "draft_revision is required to publish")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) > 64 {
		return nil, infraerrors.BadRequest("PROMPT_IDEMPOTENCY_KEY_TOO_LONG", "idempotency key must be at most 64 characters")
	}

	if idempotencyKey != "" {
		if existing, err := s.repo.GetVersionByIdempotencyKey(ctx, idempotencyKey); err == nil && existing != nil {
			return existing, nil
		}
	}

	var published *PromptTemplateVersion
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		// 锁住模板行，使并发发布的版本号分配串行化。
		if err := s.repo.LockTemplate(txCtx, templateID); err != nil {
			return err
		}
		template, err := s.repo.GetTemplate(txCtx, templateID)
		if err != nil {
			return err
		}
		if template.IsArchived() {
			return ErrPromptTemplateArchived
		}
		draft, err := s.repo.GetDraft(txCtx, templateID)
		if err != nil {
			return err
		}
		if draft.Revision != draftRevision {
			return ErrPromptRevisionConflict
		}
		if err := ValidatePromptBody(draft.Body); err != nil {
			return err
		}
		// 空范围不表示全选：发布时必须显式声明模型与 profile。
		if err := ValidatePromptScope(draft.ClientModels, draft.SupportedProfiles); err != nil {
			return err
		}
		if err := ValidatePromptProfiles(draft.SupportedProfiles); err != nil {
			return err
		}

		nextNo, err := s.repo.NextVersionNo(txCtx, templateID)
		if err != nil {
			return err
		}

		version := &PromptTemplateVersion{
			TemplateID:        templateID,
			VersionNo:         nextNo,
			Body:              draft.Body,
			BodySHA256:        PromptBodySHA256(draft.Body),
			BodyBytes:         len(draft.Body),
			ClientModels:      PromptNormalizeSet(draft.ClientModels),
			UpstreamModels:    PromptNormalizeSet(draft.UpstreamModels),
			SupportedProfiles: PromptNormalizeSet(draft.SupportedProfiles),
			ManifestSHA256:    ComputePromptManifestSHA256(draft.Body, draft.ClientModels, draft.UpstreamModels, draft.SupportedProfiles),
			ChangeNote:        strings.TrimSpace(changeNote),
			PublishedBy:       actor.ID,
			PublishedAt:       time.Now(),
		}
		if idempotencyKey != "" {
			key := idempotencyKey
			version.IdempotencyKey = &key
		}
		if err := s.repo.PublishVersion(txCtx, version); err != nil {
			return err
		}
		published = version

		event := actor.event(PromptActionPublish, PromptScopeVersion)
		event.TemplateID = &templateID
		event.VersionID = &version.ID
		event.AfterState = map[string]any{
			"version_no":         version.VersionNo,
			"body_sha256":        version.BodySHA256,
			"manifest_sha256":    version.ManifestSHA256,
			"client_models":      version.ClientModels,
			"supported_profiles": version.SupportedProfiles,
		}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
	if err != nil {
		return nil, err
	}
	return published, nil
}

// ---------------------------------------------------------------- 绑定

// GetGroupBinding 读取分组绑定；没有绑定行时返回 disabled 语义的空值。
func (s *PromptTemplateService) GetGroupBinding(ctx context.Context, groupID int64) (*GroupPromptBinding, error) {
	binding, err := s.repo.GetGroupBinding(ctx, groupID)
	if err != nil {
		if errors.Is(err, ErrPromptBindingNotFound) {
			return &GroupPromptBinding{GroupID: groupID, Mode: PromptBindingModeDisabled, Revision: 0}, nil
		}
		return nil, err
	}
	return binding, nil
}

// SetGroupBinding 设置分组绑定（乐观锁）。
func (s *PromptTemplateService) SetGroupBinding(ctx context.Context, groupID int64, expectedRevision int, input PromptBindingInput, actor PromptActor) (*GroupPromptBinding, error) {
	mode, versionID, err := s.normalizeBindingMode(input.Mode, input.VersionID)
	if err != nil {
		return nil, err
	}

	var saved *GroupPromptBinding
	err = s.repo.WithTx(ctx, func(txCtx context.Context) error {
		// 绑定到具体版本时校验版本可用：归档模板不能新增绑定。
		if mode == PromptBindingModeVersion && versionID != nil {
			version, verr := s.repo.GetVersion(txCtx, *versionID)
			if verr != nil {
				return verr
			}
			template, terr := s.repo.GetTemplate(txCtx, version.TemplateID)
			if terr != nil {
				return terr
			}
			if template.IsArchived() {
				return ErrPromptTemplateArchived
			}
		}

		binding := &GroupPromptBinding{
			GroupID:   groupID,
			Mode:      mode,
			VersionID: versionID,
			UpdatedBy: actor.ID,
		}

		current, getErr := s.repo.GetGroupBinding(txCtx, groupID)
		switch {
		case getErr == nil && current != nil:
			// 行已存在时必须匹配 revision。expectedRevision<=0 意味着调用方以为
			// “还没有配置”（例如页面加载时读到空），属于过期副本 —— 此时另一个
			// 管理员可能刚创建或改过，必须报冲突而不是静默覆盖对方。
			if current.Revision != expectedRevision {
				return ErrPromptRevisionConflict
			}
			if err := s.repo.UpsertGroupBinding(txCtx, binding, current.Revision, false); err != nil {
				return err
			}
		case errors.Is(getErr, ErrPromptBindingNotFound):
			if err := s.repo.UpsertGroupBinding(txCtx, binding, 0, true); err != nil {
				return err
			}
		default:
			return getErr
		}
		saved = binding

		event := actor.event(PromptActionBind, PromptScopeBinding)
		event.GroupID = &groupID
		event.VersionID = versionID
		event.AfterState = map[string]any{"mode": mode}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// ClearGroupBinding 删除绑定行，等价于关闭该分组。
func (s *PromptTemplateService) ClearGroupBinding(ctx context.Context, groupID int64, actor PromptActor) error {
	return s.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.DeleteGroupBinding(txCtx, groupID); err != nil {
			return err
		}
		event := actor.event(PromptActionUnbind, PromptScopeBinding)
		event.GroupID = &groupID
		event.AfterState = map[string]any{"mode": PromptBindingModeDisabled}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
}

// ---------------------------------------------------------------- 账号覆盖

// GetAccountOverride 读取账号在该分组内的覆盖。
func (s *PromptTemplateService) GetAccountOverride(ctx context.Context, accountID, groupID int64) (*AccountGroupPromptOverride, error) {
	override, err := s.repo.GetAccountOverride(ctx, accountID, groupID)
	if err != nil {
		if errors.Is(err, ErrPromptOverrideNotFound) {
			return &AccountGroupPromptOverride{AccountID: accountID, GroupID: groupID, Mode: PromptOverrideModeInherit}, nil
		}
		return nil, err
	}
	return override, nil
}

// SetAccountOverride 设置账号在该分组内的覆盖（乐观锁）。
func (s *PromptTemplateService) SetAccountOverride(ctx context.Context, accountID, groupID int64, expectedRevision int, input PromptOverrideInput, actor PromptActor) (*AccountGroupPromptOverride, error) {
	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = PromptOverrideModeInherit
	}
	var versionID *int64
	switch mode {
	case PromptOverrideModeInherit, PromptOverrideModeDisabled:
		versionID = nil
	case PromptOverrideModeVersion:
		if input.VersionID == nil {
			return nil, infraerrors.BadRequest("PROMPT_OVERRIDE_VERSION_REQUIRED", "version_id is required when mode=version")
		}
		versionID = input.VersionID
	default:
		return nil, infraerrors.BadRequest("PROMPT_OVERRIDE_MODE_INVALID", "mode must be inherit, disabled or version")
	}

	var saved *AccountGroupPromptOverride
	err := s.repo.WithTx(ctx, func(txCtx context.Context) error {
		if mode == PromptOverrideModeVersion && versionID != nil {
			if _, verr := s.repo.GetVersion(txCtx, *versionID); verr != nil {
				return verr
			}
		}

		override := &AccountGroupPromptOverride{
			AccountID: accountID,
			GroupID:   groupID,
			Mode:      mode,
			VersionID: versionID,
			UpdatedBy: actor.ID,
		}

		current, getErr := s.repo.GetAccountOverride(txCtx, accountID, groupID)
		switch {
		case getErr == nil && current != nil:
			// 行已存在时必须匹配 revision。expectedRevision<=0 意味着调用方以为
			// “还没有配置”（例如页面加载时读到空），属于过期副本 —— 此时另一个
			// 管理员可能刚创建或改过，必须报冲突而不是静默覆盖对方。
			if current.Revision != expectedRevision {
				return ErrPromptRevisionConflict
			}
			if err := s.repo.UpsertAccountOverride(txCtx, override, current.Revision, false); err != nil {
				return err
			}
		case errors.Is(getErr, ErrPromptOverrideNotFound):
			if err := s.repo.UpsertAccountOverride(txCtx, override, 0, true); err != nil {
				return err
			}
		default:
			return getErr
		}
		saved = override

		event := actor.event(PromptActionSetOverride, PromptScopeAccountOverride)
		event.AccountID = &accountID
		event.GroupID = &groupID
		event.VersionID = versionID
		event.AfterState = map[string]any{"mode": mode}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// ClearAccountOverride 删除覆盖行，回到继承分组。
func (s *PromptTemplateService) ClearAccountOverride(ctx context.Context, accountID, groupID int64, actor PromptActor) error {
	return s.repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.DeleteAccountOverride(txCtx, accountID, groupID); err != nil {
			return err
		}
		event := actor.event(PromptActionClearOverride, PromptScopeAccountOverride)
		event.AccountID = &accountID
		event.GroupID = &groupID
		event.AfterState = map[string]any{"mode": PromptOverrideModeInherit}
		return s.repo.CreateAdminEvent(txCtx, event)
	})
}

// ListAccountOverrides 列出某分组内的全部账号覆盖。
func (s *PromptTemplateService) ListAccountOverrides(ctx context.Context, groupID int64) ([]AccountGroupPromptOverride, error) {
	return s.repo.ListAccountOverridesByGroup(ctx, groupID)
}

// ---------------------------------------------------------------- 审计与预览

// ListAdminEvents 查询管理审计事件。
func (s *PromptTemplateService) ListAdminEvents(ctx context.Context, templateID, groupID *int64, limit int) ([]PromptAdminEvent, error) {
	return s.repo.ListAdminEvents(ctx, templateID, groupID, limit)
}

// ListRequestEvents 查询运行期策略记录。
//
// 用于回答“这个请求注入了没有、为什么没注入、重试时是否沿用同一版本”。
func (s *PromptTemplateService) ListRequestEvents(ctx context.Context, filter PromptRequestEventFilter) ([]PromptRequestEvent, error) {
	return s.repo.ListRequestEvents(ctx, filter)
}

// PreviewVersion 生成无上游调用的结构预览。
//
// 只做本地结构投影，不发送任何请求、不产生费用；用于管理员确认注入字段与
// “客户端原有内容是否保留”。它不预测模型行为，也不代表破甲效果。
func (s *PromptTemplateService) PreviewVersion(ctx context.Context, versionID int64) (*PromptPreviewResult, error) {
	version, err := s.repo.GetVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}

	result := &PromptPreviewResult{
		VersionID:         version.ID,
		ManifestSHA256:    version.ManifestSHA256,
		BodyBytes:         version.BodyBytes,
		ClientModels:      version.ClientModels,
		SupportedProfiles: version.SupportedProfiles,
	}

	chatSample := []byte(`{"model":"sample","messages":[{"role":"system","content":"client-system"},{"role":"user","content":"hello"}]}`)
	responsesSample := []byte(`{"model":"sample","instructions":"client-instructions","input":[{"role":"user","content":"hello"}]}`)

	for _, profile := range []string{
		PromptProfileChatHTTP,
		PromptProfileResponsesHTTP,
		PromptProfileChatToResponses,
		PromptProfileResponsesToChat,
	} {
		preview := PromptProfilePreview{
			Profile:   profile,
			Supported: PromptContainsFold(version.SupportedProfiles, profile),
		}
		sample := chatSample
		if profile == PromptProfileResponsesHTTP || profile == PromptProfileChatToResponses {
			sample = responsesSample
		}
		injected, applyErr := ApplyPromptForProfile(profile, sample, version.Body)
		if applyErr != nil {
			preview.Error = applyErr.Error()
			result.ProfilePreviews = append(result.ProfilePreviews, preview)
			continue
		}
		switch profile {
		case PromptProfileChatHTTP, PromptProfileResponsesToChat:
			preview.Field = "messages"
			preview.OriginalValue = "client-system"
			preview.InjectedValue = gjsonString(injected, "messages.1.content")
		default:
			preview.Field = "instructions"
			preview.OriginalValue = "client-instructions"
			preview.InjectedValue = gjsonString(injected, "instructions")
		}
		result.ProfilePreviews = append(result.ProfilePreviews, preview)
	}

	if len(result.ProfilePreviews) > 0 {
		// added_bytes 以 chat profile 的投影为准，表示单次注入新增的字节量级。
		result.AddedBytes = version.BodyBytes + len("\n\n")
	}
	return result, nil
}

func (s *PromptTemplateService) normalizeBindingMode(mode string, versionID *int64) (string, *int64, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = PromptBindingModeDisabled
	}
	switch mode {
	case PromptBindingModeDisabled:
		return mode, nil, nil
	case PromptBindingModeVersion:
		if versionID == nil {
			return "", nil, infraerrors.BadRequest("PROMPT_BINDING_VERSION_REQUIRED", "version_id is required when mode=version")
		}
		return mode, versionID, nil
	default:
		return "", nil, infraerrors.BadRequest("PROMPT_BINDING_MODE_INVALID", "mode must be disabled or version")
	}
}

// PromptValidationReport 汇总发布前的校验结果，供后台在提交前提示。
type PromptValidationReport struct {
	Valid             bool     `json:"valid"`
	Errors            []string `json:"errors"`
	BodyBytes         int      `json:"body_bytes"`
	BodySHA256        string   `json:"body_sha256"`
	ManifestSHA256    string   `json:"manifest_sha256"`
	ClientModels      []string `json:"client_models"`
	SupportedProfiles []string `json:"supported_profiles"`
}

// ValidateDraft 在发布前给出结构化校验结果（不写库）。
func (s *PromptTemplateService) ValidateDraft(ctx context.Context, templateID int64) (*PromptValidationReport, error) {
	draft, err := s.repo.GetDraft(ctx, templateID)
	if err != nil {
		return nil, err
	}
	report := &PromptValidationReport{
		BodyBytes:         len(draft.Body),
		BodySHA256:        PromptBodySHA256(draft.Body),
		ClientModels:      PromptNormalizeSet(draft.ClientModels),
		SupportedProfiles: PromptNormalizeSet(draft.SupportedProfiles),
	}
	if err := ValidatePromptBody(draft.Body); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	if err := ValidatePromptScope(draft.ClientModels, draft.SupportedProfiles); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	if err := ValidatePromptProfiles(draft.SupportedProfiles); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	if len(report.Errors) == 0 {
		report.Valid = true
		report.ManifestSHA256 = ComputePromptManifestSHA256(draft.Body, draft.ClientModels, draft.UpstreamModels, draft.SupportedProfiles)
	}
	return report, nil
}

func gjsonString(body []byte, path string) string {
	return gjson.GetBytes(body, path).String()
}

// promptErrorIsNotFound 便于 handler 判定 404。
func promptErrorIsNotFound(err error) bool {
	return errors.Is(err, ErrPromptTemplateNotFound) ||
		errors.Is(err, ErrPromptVersionNotFound) ||
		errors.Is(err, ErrPromptDraftNotFound) ||
		errors.Is(err, ErrPromptBindingNotFound) ||
		errors.Is(err, ErrPromptOverrideNotFound)
}
