package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// 文本提示词管理（prompt_* 表）的领域定义。
//
// 这里只描述“配置面”：模板、草稿、不可变版本、分组绑定、分组内账号覆盖、
// 管理审计。真正的请求注入在 prompt_policy.go / prompt_adapter.go 中完成。

// 分组绑定模式。
const (
	PromptBindingModeDisabled = "disabled"
	PromptBindingModeVersion  = "version"
)

// 分组内账号覆盖模式。
const (
	PromptOverrideModeInherit  = "inherit"
	PromptOverrideModeDisabled = "disabled"
	PromptOverrideModeVersion  = "version"
)

// 协议适配 profile ID。一个版本只有声明了某个 profile 才允许在该 profile 上注入。
//
// 这些 ID 对应“入站协议 → 实际出站协议”的组合，而不是单纯按模型名推断；
// 是否真的支持由 M0 的路径核实与契约测试决定。
const (
	// PromptProfileChatHTTP：Chat Completions 入站，实际出站也是 Chat Completions。
	PromptProfileChatHTTP = "chat_http"
	// PromptProfileResponsesHTTP：Responses 入站，实际出站也是 Responses。
	PromptProfileResponsesHTTP = "responses_http"
	// PromptProfileChatToResponses：Chat Completions 入站，实际出站转换为 Responses。
	PromptProfileChatToResponses = "chat_to_responses"
	// PromptProfileResponsesToChat：Responses 入站，实际出站回退为 Chat Completions。
	PromptProfileResponsesToChat = "responses_to_chat"
)

// 请求记录中的固定原因枚举。运行时只使用这些值，便于统计与告警。
const (
	PromptReasonDisabled              = "disabled"
	PromptReasonGroupDisabled         = "group_disabled"
	PromptReasonAccountDisabled       = "account_disabled"
	PromptReasonSkippedModelScope     = "skipped_model_scope"
	PromptReasonSkippedNonTextTask    = "skipped_non_text_task"
	PromptReasonApplied               = "applied"
	PromptReasonUnsupportedProfile    = "unsupported_profile"
	PromptReasonUnsupportedHistory    = "unsupported_history_mode"
	PromptReasonNoCompatibleAccount   = "no_compatible_account"
	PromptReasonConfigUnavailable     = "config_unavailable"
	PromptReasonInvalidConfig         = "invalid_config"
	PromptReasonAdapterError          = "adapter_error"
)

// 管理事件作用域。
const (
	PromptScopeTemplate        = "template"
	PromptScopeVersion         = "version"
	PromptScopeBinding         = "binding"
	PromptScopeAccountOverride = "account_override"
)

// 管理动作。
const (
	PromptActionCreate         = "create_template"
	PromptActionUpdate         = "update_template"
	PromptActionArchive        = "archive_template"
	PromptActionUpdateDraft    = "update_draft"
	PromptActionPublish        = "publish_version"
	PromptActionBind           = "set_binding"
	PromptActionUnbind         = "clear_binding"
	PromptActionSetOverride    = "set_override"
	PromptActionClearOverride  = "clear_override"
)

// PromptBodyMaxBytes 是第一阶段正文上限（32 KiB）。
const PromptBodyMaxBytes = 32 * 1024

var (
	ErrPromptTemplateNotFound   = infraerrors.NotFound("PROMPT_TEMPLATE_NOT_FOUND", "prompt template not found")
	ErrPromptTemplateArchived   = infraerrors.Conflict("PROMPT_TEMPLATE_ARCHIVED", "prompt template is archived")
	ErrPromptTemplateNameExists = infraerrors.Conflict("PROMPT_TEMPLATE_NAME_EXISTS", "an active prompt template with this name already exists")
	ErrPromptDraftNotFound      = infraerrors.NotFound("PROMPT_DRAFT_NOT_FOUND", "prompt template draft not found")
	ErrPromptVersionNotFound    = infraerrors.NotFound("PROMPT_VERSION_NOT_FOUND", "prompt template version not found")
	ErrPromptBindingNotFound    = infraerrors.NotFound("PROMPT_BINDING_NOT_FOUND", "prompt binding not found")
	ErrPromptOverrideNotFound   = infraerrors.NotFound("PROMPT_OVERRIDE_NOT_FOUND", "prompt account override not found")
	ErrPromptRevisionConflict   = infraerrors.Conflict("PROMPT_REVISION_CONFLICT", "prompt configuration was modified by another administrator")
	ErrPromptVersionImmutable   = infraerrors.Conflict("PROMPT_VERSION_IMMUTABLE", "published prompt versions are immutable")
	ErrPromptInvalidConfig      = infraerrors.BadRequest("PROMPT_INVALID_CONFIG", "prompt configuration is invalid")
	ErrPromptBodyTooLarge       = infraerrors.BadRequest("PROMPT_BODY_TOO_LARGE", "prompt body exceeds the maximum size")

	// 已启用提示词注入但无法安全解析策略：宁可拒绝请求，也不能在配置不可信的情况下
	// 悄悄按“无提示词”发送，否则后台显示启用、实际行为却与配置不一致。
	ErrPromptConfigUnavailable = infraerrors.ServiceUnavailable("PROMPT_CONFIG_UNAVAILABLE", "prompt configuration is unavailable")
	// 版本声明的 profile / 上游模型与当前出站路径不兼容：不能静默跳过注入。
	ErrPromptProfileUnsupported  = infraerrors.ServiceUnavailable("PROMPT_PROFILE_UNSUPPORTED", "prompt version does not support the outbound profile of this request")
	ErrPromptUpstreamUnsupported = infraerrors.ServiceUnavailable("PROMPT_UPSTREAM_MODEL_UNSUPPORTED", "prompt version does not apply to the selected upstream model")
	// 注入失败：请求体结构不符合该 profile 的预期。
	ErrPromptAdapterFailed = infraerrors.InternalServer("PROMPT_ADAPTER_FAILED", "failed to apply prompt to outbound request")
)

// PromptTemplate 是模板元数据（不含正文）。
type PromptTemplate struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	SourceURL   string     `json:"source_url,omitempty"`
	SourceNote  string     `json:"source_note,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	Revision    int        `json:"revision"`
	CreatedBy   *int64     `json:"created_by,omitempty"`
	UpdatedBy   *int64     `json:"updated_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// IsArchived 表示模板已归档，不能再新增绑定。
func (t PromptTemplate) IsArchived() bool { return t.ArchivedAt != nil }

// PromptTemplateDraft 是可编辑草稿。
type PromptTemplateDraft struct {
	ID                int64     `json:"id"`
	TemplateID        int64     `json:"template_id"`
	Body              string    `json:"body"`
	ClientModels      []string  `json:"client_models"`
	UpstreamModels    []string  `json:"upstream_models"`
	SupportedProfiles []string  `json:"supported_profiles"`
	Revision          int       `json:"revision"`
	UpdatedBy         *int64    `json:"updated_by,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// PromptTemplateVersion 是不可变的已发布版本。
type PromptTemplateVersion struct {
	ID                int64     `json:"id"`
	TemplateID        int64     `json:"template_id"`
	VersionNo         int       `json:"version_no"`
	Body              string    `json:"body"`
	BodySHA256        string    `json:"body_sha256"`
	BodyBytes         int       `json:"body_bytes"`
	ClientModels      []string  `json:"client_models"`
	UpstreamModels    []string  `json:"upstream_models"`
	SupportedProfiles []string  `json:"supported_profiles"`
	ManifestSHA256    string    `json:"manifest_sha256"`
	ChangeNote        string    `json:"change_note,omitempty"`
	IdempotencyKey    *string   `json:"-"`
	PublishedBy       *int64    `json:"published_by,omitempty"`
	PublishedAt       time.Time `json:"published_at"`
	CreatedAt         time.Time `json:"created_at"`
}

// GroupPromptBinding 是分组级绑定。缺少该行等价于 disabled。
type GroupPromptBinding struct {
	ID        int64     `json:"id"`
	GroupID   int64     `json:"group_id"`
	Mode      string    `json:"mode"`
	VersionID *int64    `json:"version_id,omitempty"`
	Revision  int       `json:"revision"`
	UpdatedBy *int64    `json:"updated_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Enabled 表示该分组是否启用了提示词注入。
func (b GroupPromptBinding) Enabled() bool {
	return b.Mode == PromptBindingModeVersion && b.VersionID != nil
}

// AccountGroupPromptOverride 是“账号在某个分组内”的覆盖。
type AccountGroupPromptOverride struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"account_id"`
	GroupID   int64     `json:"group_id"`
	Mode      string    `json:"mode"`
	VersionID *int64    `json:"version_id,omitempty"`
	Revision  int       `json:"revision"`
	UpdatedBy *int64    `json:"updated_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PromptAdminEvent 是管理操作审计记录（不含正文）。
type PromptAdminEvent struct {
	ID          int64          `json:"id"`
	Action      string         `json:"action"`
	Scope       string         `json:"scope"`
	TemplateID  *int64         `json:"template_id,omitempty"`
	VersionID   *int64         `json:"version_id,omitempty"`
	GroupID     *int64         `json:"group_id,omitempty"`
	AccountID   *int64         `json:"account_id,omitempty"`
	ActorID     *int64         `json:"actor_id,omitempty"`
	ActorName   string         `json:"actor_name,omitempty"`
	BeforeState map[string]any `json:"before_state,omitempty"`
	AfterState  map[string]any `json:"after_state,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
	Note        string         `json:"note,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// PromptTemplateRepository 是配置面的持久化接口。
//
// 所有写方法都返回 (found, error) 语义：调用方需要区分“不存在”和“版本冲突”，
// 因此带条件更新返回受影响行数，由服务层翻译成领域错误。
type PromptTemplateRepository interface {
	// 模板
	ListTemplates(ctx context.Context, includeArchived bool) ([]PromptTemplate, error)
	GetTemplate(ctx context.Context, id int64) (*PromptTemplate, error)
	CreateTemplate(ctx context.Context, t *PromptTemplate) error
	UpdateTemplate(ctx context.Context, t *PromptTemplate, expectedRevision int) error
	ArchiveTemplate(ctx context.Context, id int64, expectedRevision int, actor *int64) error

	// 草稿
	GetDraft(ctx context.Context, templateID int64) (*PromptTemplateDraft, error)
	UpsertDraft(ctx context.Context, d *PromptTemplateDraft, expectedRevision int, create bool) error

	// 版本
	ListVersions(ctx context.Context, templateID int64) ([]PromptTemplateVersion, error)
	GetVersion(ctx context.Context, id int64) (*PromptTemplateVersion, error)
	GetVersionByIdempotencyKey(ctx context.Context, key string) (*PromptTemplateVersion, error)
	PublishVersion(ctx context.Context, v *PromptTemplateVersion) error
	NextVersionNo(ctx context.Context, templateID int64) (int, error)
	// LockTemplate 在事务内对模板行加 FOR UPDATE 锁，使并发发布串行分配版本号。
	// 必须在 WithTx 内调用，否则没有实际意义。
	LockTemplate(ctx context.Context, id int64) error

	// 绑定
	GetGroupBinding(ctx context.Context, groupID int64) (*GroupPromptBinding, error)
	UpsertGroupBinding(ctx context.Context, b *GroupPromptBinding, expectedRevision int, create bool) error
	DeleteGroupBinding(ctx context.Context, groupID int64) error
	ListBindingsByGroupIDs(ctx context.Context, groupIDs []int64) ([]GroupPromptBinding, error)

	// 账号覆盖
	GetAccountOverride(ctx context.Context, accountID, groupID int64) (*AccountGroupPromptOverride, error)
	UpsertAccountOverride(ctx context.Context, o *AccountGroupPromptOverride, expectedRevision int, create bool) error
	DeleteAccountOverride(ctx context.Context, accountID, groupID int64) error
	ListAccountOverridesByGroup(ctx context.Context, groupID int64) ([]AccountGroupPromptOverride, error)

	// 审计
	CreateAdminEvent(ctx context.Context, e *PromptAdminEvent) error
	ListAdminEvents(ctx context.Context, templateID *int64, groupID *int64, limit int) ([]PromptAdminEvent, error)

	// 运行期记录（尽力而为）
	CreateRequestEvent(ctx context.Context, e *PromptRequestEvent) error
	ListRequestEvents(ctx context.Context, filter PromptRequestEventFilter) ([]PromptRequestEvent, *pagination.PaginationResult, error)

	// WithTx 在同一个数据库事务内执行 fn。fn 内部的仓储调用会自动复用该事务，
	// 因此“配置修改 + 管理审计”可以原子提交，不会出现改了配置却没有审计记录
	// （或反之）的中间状态。嵌套调用会复用已存在的事务。
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// PromptNormalizeSet 把用户输入的模型/profile 列表规范化：去空白、去重、排序。
//
// 排序是 manifest 摘要可复现的前提；空集合保持为空，不表示“全选”。
func PromptNormalizeSet(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// PromptContainsFold 判断集合中是否包含某个模型名（大小写不敏感精确匹配）。
func PromptContainsFold(set []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range set {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

// PromptBodySHA256 返回正文原始 UTF-8 字节的 SHA-256（小写十六进制）。
func PromptBodySHA256(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// PromptManifestPayload 是 manifest 摘要的确定性序列化结构。
//
// 使用 struct 而不是 map：encoding/json 对 struct 字段顺序稳定，对 map 会按键排序
// 但结构不明确。集合在调用前已排序去重，因此同一份配置永远得到同一个摘要。
type PromptManifestPayload struct {
	BodySHA256        string   `json:"body_sha256"`
	BodyBytes         int      `json:"body_bytes"`
	ClientModels      []string `json:"client_models"`
	UpstreamModels    []string `json:"upstream_models"`
	SupportedProfiles []string `json:"supported_profiles"`
}

// ComputePromptManifestSHA256 计算版本 manifest 摘要。
//
// 该摘要覆盖正文与全部适用范围，因此“同一个 manifest”意味着运行行为等价；
// 正文相同但 profile 集合不同的两个版本会得到不同摘要，不会被误判为同一策略。
func ComputePromptManifestSHA256(body string, clientModels, upstreamModels, supportedProfiles []string) string {
	payload := PromptManifestPayload{
		BodySHA256:        PromptBodySHA256(body),
		BodyBytes:         len(body),
		ClientModels:      PromptNormalizeSet(clientModels),
		UpstreamModels:    PromptNormalizeSet(upstreamModels),
		SupportedProfiles: PromptNormalizeSet(supportedProfiles),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// struct 只包含可序列化字段，此处不会失败；保留兜底避免返回空摘要。
		encoded = []byte(payload.BodySHA256)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// ValidatePromptBody 校验正文：非空、UTF-8 有效、不超过上限。
func ValidatePromptBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return infraerrors.BadRequest("PROMPT_BODY_EMPTY", "prompt body must not be empty")
	}
	if !utf8.ValidString(body) {
		return infraerrors.BadRequest("PROMPT_BODY_INVALID_UTF8", "prompt body must be valid UTF-8")
	}
	if len(body) > PromptBodyMaxBytes {
		return ErrPromptBodyTooLarge
	}
	return nil
}

// ValidatePromptScope 校验适用范围：必须显式声明至少一个模型和一个 profile。
//
// 空集合不表示“全选”，否则一个漏填的草稿会在所有模型上生效。
func ValidatePromptScope(clientModels, supportedProfiles []string) error {
	if len(PromptNormalizeSet(clientModels)) == 0 {
		return infraerrors.BadRequest("PROMPT_SCOPE_EMPTY_MODELS", "at least one client model must be declared")
	}
	if len(PromptNormalizeSet(supportedProfiles)) == 0 {
		return infraerrors.BadRequest("PROMPT_SCOPE_EMPTY_PROFILES", "at least one supported profile must be declared")
	}
	return nil
}

// IsKnownPromptProfile 判断 profile ID 是否属于已知集合。
func IsKnownPromptProfile(profile string) bool {
	switch profile {
	case PromptProfileChatHTTP,
		PromptProfileResponsesHTTP,
		PromptProfileChatToResponses,
		PromptProfileResponsesToChat:
		return true
	default:
		return false
	}
}

// ValidatePromptProfiles 拒绝未知 profile ID，避免配置里出现永远不会命中的拼写错误。
func ValidatePromptProfiles(profiles []string) error {
	normalized := PromptNormalizeSet(profiles)
	for _, p := range normalized {
		if !IsKnownPromptProfile(p) {
			return infraerrors.BadRequest("PROMPT_UNKNOWN_PROFILE", "unknown prompt profile: "+p)
		}
	}
	return nil
}
