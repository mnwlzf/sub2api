package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// 策略解析与冻结。
//
// 关键语义（设计文档第 6 节）：
//   - “逻辑请求”= 一次下游 HTTP 请求，它的所有上游尝试（含换号重试）共享同一份
//     冻结策略，因此不会出现“第一次用版本 A、重试到另一个账号时静默换成版本 B”。
//   - 冻结发生在首次网络发送之前；每次尝试都从不可变原始 body 派生并注入，
//     所以重复尝试不会累积多条 system 消息。
//   - 后台“已启用”与运行期“实际注入”必须一致：配置不可信时返回错误，而不是
//     静默按未启用发送。

// 绑定来源，用于请求记录与排障。
const (
	PromptBindingSourceGroup           = "group"
	PromptBindingSourceAccountOverride = "account_override"
)

// FrozenPromptPolicy 是一次逻辑请求内不可变的提示词策略。
type FrozenPromptPolicy struct {
	// Enabled 为 false 时 Reason 说明为什么没有注入。
	Enabled            bool     `json:"enabled"`
	Reason             string   `json:"reason"`
	TemplateID         int64    `json:"template_id,omitempty"`
	VersionID          int64    `json:"version_id,omitempty"`
	ManifestSHA256     string   `json:"manifest_sha256,omitempty"`
	Body               string   `json:"-"`
	SupportedProfiles  []string `json:"supported_profiles,omitempty"`
	ClientModels       []string `json:"client_models,omitempty"`
	UpstreamModels     []string `json:"upstream_models,omitempty"`
	GroupID            int64    `json:"group_id,omitempty"`
	AccountID          int64    `json:"account_id,omitempty"`
	BindingSource      string   `json:"binding_source,omitempty"`
}

// disabledPromptPolicy 构造一个“不注入”的策略，并带上原因。
func disabledPromptPolicy(reason string) *FrozenPromptPolicy {
	return &FrozenPromptPolicy{Enabled: false, Reason: reason}
}

const promptPolicyGinKey = "sub2api.frozen_prompt_policy"

// WithFrozenPromptPolicy 把冻结策略写入 Gin context。
//
// 由 handler 在重试循环之前调用一次；服务层各出站路径只读取，不再重新解析，
// 因此账号 failover 不会改变策略身份。
func WithFrozenPromptPolicy(c *gin.Context, policy *FrozenPromptPolicy) {
	if c == nil || policy == nil {
		return
	}
	c.Set(promptPolicyGinKey, policy)
}

// FrozenPromptPolicyFromGin 读取冻结策略；没有冻结过时返回 nil。
func FrozenPromptPolicyFromGin(c *gin.Context) *FrozenPromptPolicy {
	if c == nil {
		return nil
	}
	value, ok := c.Get(promptPolicyGinKey)
	if !ok {
		return nil
	}
	policy, _ := value.(*FrozenPromptPolicy)
	return policy
}

// PromptPolicyResolver 依据分组绑定与账号覆盖解析出冻结策略。
type PromptPolicyResolver struct {
	repo      PromptTemplateRepository
	enabledFn func() bool
}

// NewPromptPolicyResolver 构造解析器。cfg 为 nil 时视为功能关闭。
func NewPromptPolicyResolver(repo PromptTemplateRepository, cfg *config.Config) *PromptPolicyResolver {
	return &PromptPolicyResolver{
		repo: repo,
		enabledFn: func() bool {
			return cfg != nil && cfg.Gateway.TextPromptInjectionEnabled
		},
	}
}

// NewPromptPolicyResolverWithFlag 用于测试与显式开关场景。
func NewPromptPolicyResolverWithFlag(repo PromptTemplateRepository, enabledFn func() bool) *PromptPolicyResolver {
	return &PromptPolicyResolver{repo: repo, enabledFn: enabledFn}
}

// Enabled 表示部署级总开关是否打开。
func (r *PromptPolicyResolver) Enabled() bool {
	if r == nil || r.enabledFn == nil {
		return false
	}
	return r.enabledFn()
}

// Resolve 解析出本次逻辑请求的冻结策略。
//
// 返回的 policy 永不为 nil。返回 error 仅用于“已启用但无法安全判定”的情况，
// 调用方应把该错误按 503 处理，而不是继续按未注入发送。
func (r *PromptPolicyResolver) Resolve(ctx context.Context, groupID, accountID int64, clientModel string) (*FrozenPromptPolicy, error) {
	if !r.Enabled() {
		return disabledPromptPolicy(PromptReasonDisabled), nil
	}
	if r.repo == nil {
		return nil, ErrPromptConfigUnavailable
	}
	if groupID <= 0 {
		// 没有分组就没有绑定可言，等价于该分组未启用。
		return disabledPromptPolicy(PromptReasonGroupDisabled), nil
	}

	binding, err := r.repo.GetGroupBinding(ctx, groupID)
	if err != nil {
		if errors.Is(err, ErrPromptBindingNotFound) {
			return disabledPromptPolicy(PromptReasonGroupDisabled), nil
		}
		return nil, fmt.Errorf("%w: read group binding: %v", ErrPromptConfigUnavailable, err)
	}
	if !binding.Enabled() {
		return disabledPromptPolicy(PromptReasonGroupDisabled), nil
	}

	versionID := *binding.VersionID
	source := PromptBindingSourceGroup

	if accountID > 0 {
		override, overrideErr := r.repo.GetAccountOverride(ctx, accountID, groupID)
		switch {
		case overrideErr == nil && override != nil:
			switch override.Mode {
			case PromptOverrideModeDisabled:
				// 分组启用、该账号显式关闭：这是账号级选择，不是配置故障。
				return disabledPromptPolicy(PromptReasonAccountDisabled), nil
			case PromptOverrideModeVersion:
				if override.VersionID != nil {
					versionID = *override.VersionID
					source = PromptBindingSourceAccountOverride
				}
			}
		case overrideErr != nil && !errors.Is(overrideErr, ErrPromptOverrideNotFound):
			return nil, fmt.Errorf("%w: read account override: %v", ErrPromptConfigUnavailable, overrideErr)
		}
	}

	version, err := r.repo.GetVersion(ctx, versionID)
	if err != nil {
		// 绑定指向不存在的版本属于数据完整性问题：不能退化成“不注入”。
		return nil, fmt.Errorf("%w: read version %d: %v", ErrPromptConfigUnavailable, versionID, err)
	}

	// 客户端模型不在范围内时不注入；这是正常的“不适用”，不是故障。
	if !PromptContainsFold(version.ClientModels, clientModel) {
		return disabledPromptPolicy(PromptReasonSkippedModelScope), nil
	}

	return &FrozenPromptPolicy{
		Enabled:           true,
		Reason:            PromptReasonApplied,
		TemplateID:        version.TemplateID,
		VersionID:         version.ID,
		ManifestSHA256:    version.ManifestSHA256,
		Body:              version.Body,
		SupportedProfiles: version.SupportedProfiles,
		ClientModels:      version.ClientModels,
		UpstreamModels:    version.UpstreamModels,
		GroupID:           groupID,
		AccountID:         accountID,
		BindingSource:     source,
	}, nil
}

// ApplyFrozenPromptInjection 在最终出站前应用冻结策略。
//
// 调用位置必须是“最后一次改写 body 之前、http.NewRequest 之前”，这样注入内容
// 与最终发送字节完全一致，签名/缓存键也能基于最终 body 计算。
//
// 返回值语义：
//   - 功能关闭、未冻结、策略未启用 → 原样返回 body 且 changed=false（走原路径，
//     调用方不应重建 requestView 等派生结构，避免热路径额外开销）。
//   - 该出站路径不在支持范围（profile 非已知值）→ 原样返回，保持既有行为。
//   - 策略启用但 profile/上游模型不兼容 → 返回错误（不静默跳过）。
//   - 请求体不适配该 profile → 返回 ErrPromptAdapterFailed。
func (s *OpenAIGatewayService) ApplyFrozenPromptInjection(c *gin.Context, profile string, upstreamModel string, body []byte) ([]byte, bool, error) {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.TextPromptInjectionEnabled {
		return body, false, nil
	}
	policy := FrozenPromptPolicyFromGin(c)
	if policy == nil || !policy.Enabled {
		return body, false, nil
	}
	// 该出站路径本身不在本功能的支持范围内（例如 Anthropic Messages→Chat 回退）：
	// 保留既有行为并记录跳过原因。管理员无法在版本里声明一个不存在的 profile，
	// 因此这里报错只会打断与该配置无关的流量。
	if !IsKnownPromptProfile(profile) {
		return body, false, nil
	}
	if !PromptContainsFold(policy.SupportedProfiles, profile) {
		return nil, false, fmt.Errorf("%w: version %d does not allow profile %s", ErrPromptProfileUnsupported, policy.VersionID, profile)
	}
	if len(policy.UpstreamModels) > 0 && !PromptContainsFold(policy.UpstreamModels, upstreamModel) {
		return nil, false, fmt.Errorf("%w: version %d does not apply to upstream model %s", ErrPromptUpstreamUnsupported, policy.VersionID, upstreamModel)
	}
	if PromptBodyTargetsImageGeneration(body) {
		// 非纯文本任务：按设计跳过，而不是注入后改变图片工具行为。
		return body, false, nil
	}
	next, err := ApplyPromptForProfile(profile, body, policy.Body)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrPromptAdapterFailed, err)
	}
	return next, true, nil
}

// PromptPolicySummary 返回用于日志/审计的简短描述，不含正文。
func PromptPolicySummary(policy *FrozenPromptPolicy) string {
	if policy == nil {
		return "policy=none"
	}
	if !policy.Enabled {
		return "policy=disabled reason=" + policy.Reason
	}
	return fmt.Sprintf(
		"policy=enabled version_id=%d manifest=%s source=%s group_id=%d account_id=%d",
		policy.VersionID,
		shortPromptHash(policy.ManifestSHA256),
		policy.BindingSource,
		policy.GroupID,
		policy.AccountID,
	)
}

func shortPromptHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
