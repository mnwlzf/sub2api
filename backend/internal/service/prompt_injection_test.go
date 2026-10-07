//go:build unit

package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------- 协议适配器

func TestApplyPromptToChatBody_InsertsAfterLeadingSystemMessages(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"client-system"},
		{"role":"developer","content":"client-developer"},
		{"role":"user","content":"hello"}
	]}`)
	out, err := ApplyPromptToChatBody(body, "server-prompt")
	require.NoError(t, err)

	require.Equal(t, "system", gjson.GetBytes(out, "messages.0.role").String())
	require.Equal(t, "client-system", gjson.GetBytes(out, "messages.0.content").String())
	require.Equal(t, "developer", gjson.GetBytes(out, "messages.1.role").String())
	require.Equal(t, "client-developer", gjson.GetBytes(out, "messages.1.content").String())
	// 注入的消息落在前导系统消息之后、普通对话之前。
	require.Equal(t, "system", gjson.GetBytes(out, "messages.2.role").String())
	require.Equal(t, "server-prompt", gjson.GetBytes(out, "messages.2.content").String())
	require.Equal(t, "user", gjson.GetBytes(out, "messages.3.role").String())
	require.Equal(t, "hello", gjson.GetBytes(out, "messages.3.content").String())
	require.Equal(t, 4, len(gjson.GetBytes(out, "messages").Array()))
}

func TestApplyPromptToChatBody_NoLeadingSystemMessagePrepends(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, err := ApplyPromptToChatBody(body, "server-prompt")
	require.NoError(t, err)
	require.Equal(t, "server-prompt", gjson.GetBytes(out, "messages.0.content").String())
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.1.content").String())
}

func TestApplyPromptToChatBody_PreservesToolAndMultimodalStructures(t *testing.T) {
	body := []byte(`{"model":"m","temperature":0.3,"tools":[{"type":"function","function":{"name":"f"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]}`)
	out, err := ApplyPromptToChatBody(body, "server-prompt")
	require.NoError(t, err)

	require.Equal(t, "server-prompt", gjson.GetBytes(out, "messages.0.content").String())
	// 原有消息整体后移，且多模态 content 数组与 tool_calls 结构保持不变。
	require.Equal(t, "text", gjson.GetBytes(out, "messages.1.content.0.type").String())
	require.Equal(t, "data:image/png;base64,AAA", gjson.GetBytes(out, "messages.1.content.1.image_url.url").String())
	require.Equal(t, "call_1", gjson.GetBytes(out, "messages.2.tool_calls.0.id").String())
	require.Equal(t, "call_1", gjson.GetBytes(out, "messages.3.tool_call_id").String())
	// 非目标字段不被改动。
	require.Equal(t, 0.3, gjson.GetBytes(out, "temperature").Float())
	require.Equal(t, "f", gjson.GetBytes(out, "tools.0.function.name").String())
}

func TestApplyPromptToChatBody_IsNotAccumulativeWhenDerivedFromOriginal(t *testing.T) {
	// 关键性质：每次都从不可变原始 body 派生，所以重试不会累积多条 system 消息。
	original := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	first, err := ApplyPromptToChatBody(original, "server-prompt")
	require.NoError(t, err)
	second, err := ApplyPromptToChatBody(original, "server-prompt")
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
	require.Equal(t, 2, len(gjson.GetBytes(second, "messages").Array()))
}

func TestApplyPromptToChatBody_EmptyMessagesArray(t *testing.T) {
	out, err := ApplyPromptToChatBody([]byte(`{"model":"m","messages":[]}`), "server-prompt")
	require.NoError(t, err)
	require.Equal(t, 1, len(gjson.GetBytes(out, "messages").Array()))
	require.Equal(t, "server-prompt", gjson.GetBytes(out, "messages.0.content").String())
}

func TestApplyPromptToChatBody_RejectsBodyWithoutMessages(t *testing.T) {
	_, err := ApplyPromptToChatBody([]byte(`{"model":"m","input":"x"}`), "server-prompt")
	require.Error(t, err)
	_, err = ApplyPromptToChatBody([]byte(`not json`), "server-prompt")
	require.Error(t, err)
}

func TestApplyPromptToChatBody_EmptyPromptIsNoop(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, err := ApplyPromptToChatBody(body, "")
	require.NoError(t, err)
	require.Equal(t, string(body), string(out))
}

func TestApplyPromptToResponsesBody_AppendsToExistingInstructions(t *testing.T) {
	body := []byte(`{"model":"m","instructions":"client-system","input":[{"role":"user","content":"hi"}]}`)
	out, err := ApplyPromptToResponsesBody(body, "server-prompt")
	require.NoError(t, err)
	// 原有 instructions 在前，服务端提示词在后，空行分隔。
	require.Equal(t, "client-system\n\nserver-prompt", gjson.GetBytes(out, "instructions").String())
	// input 不被改动。
	require.Equal(t, "hi", gjson.GetBytes(out, "input.0.content").String())
}

func TestApplyPromptToResponsesBody_SetsInstructionsWhenAbsent(t *testing.T) {
	out, err := ApplyPromptToResponsesBody([]byte(`{"model":"m","input":[]}`), "server-prompt")
	require.NoError(t, err)
	require.Equal(t, "server-prompt", gjson.GetBytes(out, "instructions").String())
}

func TestApplyPromptToResponsesBody_RejectsNonStringInstructions(t *testing.T) {
	_, err := ApplyPromptToResponsesBody([]byte(`{"model":"m","instructions":[{"type":"text","text":"x"}]}`), "server-prompt")
	require.Error(t, err)
}

func TestApplyPromptForProfile_DispatchesByProfile(t *testing.T) {
	chat := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	responses := []byte(`{"model":"m","input":[]}`)

	out, err := ApplyPromptForProfile(PromptProfileChatHTTP, chat, "p")
	require.NoError(t, err)
	require.Equal(t, "p", gjson.GetBytes(out, "messages.0.content").String())

	out, err = ApplyPromptForProfile(PromptProfileResponsesToChat, chat, "p")
	require.NoError(t, err)
	require.Equal(t, "p", gjson.GetBytes(out, "messages.0.content").String())

	out, err = ApplyPromptForProfile(PromptProfileResponsesHTTP, responses, "p")
	require.NoError(t, err)
	require.Equal(t, "p", gjson.GetBytes(out, "instructions").String())

	out, err = ApplyPromptForProfile(PromptProfileChatToResponses, responses, "p")
	require.NoError(t, err)
	require.Equal(t, "p", gjson.GetBytes(out, "instructions").String())

	_, err = ApplyPromptForProfile("unknown_profile", chat, "p")
	require.Error(t, err)
}

func TestPromptBodyTargetsImageGeneration(t *testing.T) {
	require.True(t, PromptBodyTargetsImageGeneration([]byte(`{"model":"m","tools":[{"type":"image_generation"}]}`)))
	require.True(t, PromptBodyTargetsImageGeneration([]byte(`{"model":"m","tool_choice":{"type":"image_generation"}}`)))
	require.False(t, PromptBodyTargetsImageGeneration([]byte(`{"model":"m","tools":[{"type":"function"}]}`)))
	require.False(t, PromptBodyTargetsImageGeneration([]byte(`{"model":"m","messages":[]}`)))
}

// ---------------------------------------------------------------- manifest 摘要

func TestComputePromptManifestSHA256_IsStableAndScopeSensitive(t *testing.T) {
	a := ComputePromptManifestSHA256("body", []string{"m2", "m1"}, nil, []string{PromptProfileChatHTTP})
	// 集合顺序不影响摘要（内部排序去重）。
	b := ComputePromptManifestSHA256("body", []string{"m1", "m2"}, nil, []string{PromptProfileChatHTTP})
	require.Equal(t, a, b)

	// 正文变化 → 摘要变化。
	require.NotEqual(t, a, ComputePromptManifestSHA256("body2", []string{"m1", "m2"}, nil, []string{PromptProfileChatHTTP}))
	// 适用范围变化 → 摘要变化（同一正文、不同 profile 不是同一个策略）。
	require.NotEqual(t, a, ComputePromptManifestSHA256("body", []string{"m1", "m2"}, nil, []string{PromptProfileResponsesHTTP}))
}

func TestValidatePromptBodyAndScope(t *testing.T) {
	require.Error(t, ValidatePromptBody("   "))
	require.NoError(t, ValidatePromptBody("hello"))
	require.Error(t, ValidatePromptBody(string(make([]byte, PromptBodyMaxBytes+1))))

	require.Error(t, ValidatePromptScope(nil, []string{PromptProfileChatHTTP}))
	require.Error(t, ValidatePromptScope([]string{"m"}, nil))
	require.NoError(t, ValidatePromptScope([]string{"m"}, []string{PromptProfileChatHTTP}))

	require.NoError(t, ValidatePromptProfiles([]string{PromptProfileChatHTTP}))
	require.Error(t, ValidatePromptProfiles([]string{"bogus"}))
}

// ---------------------------------------------------------------- 策略解析

// fakePromptRepo 是解析器测试用的最小仓储实现；只填充测试关心的字段。
type fakePromptRepo struct {
	binding       *GroupPromptBinding
	bindingErr    error
	override      *AccountGroupPromptOverride
	overrideErr   error
	version       *PromptTemplateVersion
	versionErr    error
	lastVersionID int64
}

func (f *fakePromptRepo) ListTemplates(context.Context, bool) ([]PromptTemplate, error) {
	return nil, nil
}
func (f *fakePromptRepo) GetTemplate(context.Context, int64) (*PromptTemplate, error) {
	return nil, ErrPromptTemplateNotFound
}
func (f *fakePromptRepo) CreateTemplate(context.Context, *PromptTemplate) error { return nil }
func (f *fakePromptRepo) UpdateTemplate(context.Context, *PromptTemplate, int) error {
	return nil
}
func (f *fakePromptRepo) ArchiveTemplate(context.Context, int64, int, *int64) error { return nil }
func (f *fakePromptRepo) GetDraft(context.Context, int64) (*PromptTemplateDraft, error) {
	return nil, ErrPromptDraftNotFound
}
func (f *fakePromptRepo) UpsertDraft(context.Context, *PromptTemplateDraft, int, bool) error {
	return nil
}
func (f *fakePromptRepo) ListVersions(context.Context, int64) ([]PromptTemplateVersion, error) {
	return nil, nil
}
func (f *fakePromptRepo) GetVersion(_ context.Context, id int64) (*PromptTemplateVersion, error) {
	f.lastVersionID = id
	if f.versionErr != nil {
		return nil, f.versionErr
	}
	if f.version == nil {
		return nil, ErrPromptVersionNotFound
	}
	return f.version, nil
}
func (f *fakePromptRepo) GetVersionByIdempotencyKey(context.Context, string) (*PromptTemplateVersion, error) {
	return nil, ErrPromptVersionNotFound
}
func (f *fakePromptRepo) PublishVersion(context.Context, *PromptTemplateVersion) error { return nil }
func (f *fakePromptRepo) NextVersionNo(context.Context, int64) (int, error)           { return 1, nil }
func (f *fakePromptRepo) LockTemplate(context.Context, int64) error                   { return nil }
func (f *fakePromptRepo) GetGroupBinding(context.Context, int64) (*GroupPromptBinding, error) {
	if f.bindingErr != nil {
		return nil, f.bindingErr
	}
	if f.binding == nil {
		return nil, ErrPromptBindingNotFound
	}
	return f.binding, nil
}
func (f *fakePromptRepo) UpsertGroupBinding(context.Context, *GroupPromptBinding, int, bool) error {
	return nil
}
func (f *fakePromptRepo) DeleteGroupBinding(context.Context, int64) error { return nil }
func (f *fakePromptRepo) ListBindingsByGroupIDs(context.Context, []int64) ([]GroupPromptBinding, error) {
	return nil, nil
}
func (f *fakePromptRepo) GetAccountOverride(context.Context, int64, int64) (*AccountGroupPromptOverride, error) {
	if f.overrideErr != nil {
		return nil, f.overrideErr
	}
	if f.override == nil {
		return nil, ErrPromptOverrideNotFound
	}
	return f.override, nil
}
func (f *fakePromptRepo) UpsertAccountOverride(context.Context, *AccountGroupPromptOverride, int, bool) error {
	return nil
}
func (f *fakePromptRepo) DeleteAccountOverride(context.Context, int64, int64) error { return nil }
func (f *fakePromptRepo) ListAccountOverridesByGroup(context.Context, int64) ([]AccountGroupPromptOverride, error) {
	return nil, nil
}
func (f *fakePromptRepo) CreateAdminEvent(context.Context, *PromptAdminEvent) error { return nil }
func (f *fakePromptRepo) ListAdminEvents(context.Context, *int64, *int64, int) ([]PromptAdminEvent, error) {
	return nil, nil
}
func (f *fakePromptRepo) CreateRequestEvent(context.Context, *PromptRequestEvent) error { return nil }
func (f *fakePromptRepo) ListRequestEvents(context.Context, PromptRequestEventFilter) ([]PromptRequestEvent, error) {
	return nil, nil
}
func (f *fakePromptRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func enabledVersion() *PromptTemplateVersion {
	return &PromptTemplateVersion{
		ID:                7,
		TemplateID:        3,
		VersionNo:         2,
		Body:              "server-prompt",
		ClientModels:      []string{"gpt-6.1-sol"},
		SupportedProfiles: []string{PromptProfileChatHTTP},
		ManifestSHA256:    "abc123abc123abc123",
	}
}

func versionBinding() *GroupPromptBinding {
	versionID := int64(7)
	return &GroupPromptBinding{GroupID: 11, Mode: PromptBindingModeVersion, VersionID: &versionID, Revision: 2}
}

func TestPromptPolicyResolver_DisabledWhenFeatureOff(t *testing.T) {
	repo := &fakePromptRepo{binding: versionBinding(), version: enabledVersion()}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return false })

	policy, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.NoError(t, err)
	require.False(t, policy.Enabled)
	require.Equal(t, PromptReasonDisabled, policy.Reason)
}

func TestPromptPolicyResolver_GroupWithoutBindingIsDisabled(t *testing.T) {
	repo := &fakePromptRepo{}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	policy, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.NoError(t, err)
	require.False(t, policy.Enabled)
	require.Equal(t, PromptReasonGroupDisabled, policy.Reason)
}

func TestPromptPolicyResolver_AppliesBoundVersion(t *testing.T) {
	repo := &fakePromptRepo{binding: versionBinding(), version: enabledVersion()}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	policy, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.NoError(t, err)
	require.True(t, policy.Enabled)
	require.Equal(t, int64(7), policy.VersionID)
	require.Equal(t, "server-prompt", policy.Body)
	require.Equal(t, PromptBindingSourceGroup, policy.BindingSource)
}

func TestPromptPolicyResolver_ModelScopeMismatchSkips(t *testing.T) {
	repo := &fakePromptRepo{binding: versionBinding(), version: enabledVersion()}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	policy, err := resolver.Resolve(context.Background(), 11, 22, "some-other-model")
	require.NoError(t, err)
	require.False(t, policy.Enabled)
	require.Equal(t, PromptReasonSkippedModelScope, policy.Reason)
}

func TestPromptPolicyResolver_AccountOverrideDisabledWinsOverGroup(t *testing.T) {
	repo := &fakePromptRepo{
		binding:  versionBinding(),
		version:  enabledVersion(),
		override: &AccountGroupPromptOverride{AccountID: 22, GroupID: 11, Mode: PromptOverrideModeDisabled},
	}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	policy, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.NoError(t, err)
	require.False(t, policy.Enabled)
	require.Equal(t, PromptReasonAccountDisabled, policy.Reason)
}

func TestPromptPolicyResolver_AccountOverrideVersionWins(t *testing.T) {
	overrideVersionID := int64(9)
	overrideVersion := enabledVersion()
	overrideVersion.ID = 9
	overrideVersion.Body = "override-prompt"

	repo := &fakePromptRepo{
		binding:  versionBinding(),
		version:  overrideVersion,
		override: &AccountGroupPromptOverride{AccountID: 22, GroupID: 11, Mode: PromptOverrideModeVersion, VersionID: &overrideVersionID},
	}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	policy, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.NoError(t, err)
	require.True(t, policy.Enabled)
	require.Equal(t, int64(9), policy.VersionID)
	require.Equal(t, "override-prompt", policy.Body)
	require.Equal(t, PromptBindingSourceAccountOverride, policy.BindingSource)
	require.Equal(t, int64(9), repo.lastVersionID)
}

func TestPromptPolicyResolver_MissingVersionIsNotSilentlySkipped(t *testing.T) {
	// 绑定指向不存在的版本属于配置完整性问题：必须报错，而不是按“不注入”发送。
	repo := &fakePromptRepo{binding: versionBinding(), versionErr: ErrPromptVersionNotFound}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	_, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPromptConfigUnavailable))
}

func TestPromptPolicyResolver_BindingReadFailureIsNotSilentlySkipped(t *testing.T) {
	repo := &fakePromptRepo{bindingErr: errors.New("db down")}
	resolver := NewPromptPolicyResolverWithFlag(repo, func() bool { return true })

	_, err := resolver.Resolve(context.Background(), 11, 22, "gpt-6.1-sol")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPromptConfigUnavailable))
}

// ---------------------------------------------------------------- 出站注入

func newPromptTestService(enabled bool) *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{TextPromptInjectionEnabled: enabled}}}
}

func newPromptTestGinContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// ---------------------------------------------------------------- 乐观锁回归

func TestSetGroupBinding_StaleEmptyRevisionConflicts(t *testing.T) {
	// 回归：页面读到“还没有绑定”（revision 0），随后另一个管理员创建了绑定
	// （revision 1）。此时用 revision 0 提交必须报冲突，而不是静默覆盖对方。
	repo := &fakePromptRepo{binding: versionBinding(), version: enabledVersion()}
	svc := NewPromptTemplateService(repo)

	_, err := svc.SetGroupBinding(context.Background(), 11, 0,
		PromptBindingInput{Mode: PromptBindingModeDisabled}, PromptActor{})
	require.ErrorIs(t, err, ErrPromptRevisionConflict)
}

func TestSetGroupBinding_MatchingRevisionSucceeds(t *testing.T) {
	binding := versionBinding() // revision 2
	repo := &fakePromptRepo{binding: binding, version: enabledVersion()}
	svc := NewPromptTemplateService(repo)

	saved, err := svc.SetGroupBinding(context.Background(), 11, binding.Revision,
		PromptBindingInput{Mode: PromptBindingModeDisabled}, PromptActor{})
	require.NoError(t, err)
	require.Equal(t, PromptBindingModeDisabled, saved.Mode)
}

func TestSetAccountOverride_StaleEmptyRevisionConflicts(t *testing.T) {
	repo := &fakePromptRepo{
		binding:  versionBinding(),
		version:  enabledVersion(),
		override: &AccountGroupPromptOverride{AccountID: 22, GroupID: 11, Mode: PromptOverrideModeInherit, Revision: 3},
	}
	svc := NewPromptTemplateService(repo)

	_, err := svc.SetAccountOverride(context.Background(), 22, 11, 0,
		PromptOverrideInput{Mode: PromptOverrideModeDisabled}, PromptActor{})
	require.ErrorIs(t, err, ErrPromptRevisionConflict)
}

func TestApplyFrozenPromptInjection_FeatureOffIsNoop(t *testing.T) {
	svc := newPromptTestService(false)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{Enabled: true, Body: "server-prompt", SupportedProfiles: []string{PromptProfileChatHTTP}})

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestApplyFrozenPromptInjection_NoFrozenPolicyIsNoop(t *testing.T) {
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestApplyFrozenPromptInjection_AppliesChatProfile(t *testing.T) {
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"m","messages":[{"role":"system","content":"client"},{"role":"user","content":"hi"}]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "client", gjson.GetBytes(out, "messages.0.content").String())
	require.Equal(t, "server-prompt", gjson.GetBytes(out, "messages.1.content").String())
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.2.content").String())
}

func TestApplyFrozenPromptInjection_AppliesResponsesProfile(t *testing.T) {
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileResponsesHTTP},
	})

	body := []byte(`{"model":"m","instructions":"client","input":[]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileResponsesHTTP, "m", body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "client\n\nserver-prompt", gjson.GetBytes(out, "instructions").String())
}

func TestApplyFrozenPromptInjection_ProfileNotAllowedIsError(t *testing.T) {
	// 版本只声明 chat_http，却走到了 responses_http：必须报错，不能静默不注入。
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"m","input":[]}`)
	_, _, err := svc.ApplyFrozenPromptInjection(c, PromptProfileResponsesHTTP, "m", body)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPromptProfileUnsupported))
}

func TestApplyFrozenPromptInjection_UnknownProfileKeepsLegacyBehavior(t *testing.T) {
	// Messages→Chat 这类路径不在支持范围内：保持既有行为，不因配置而报错。
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, "", "m", body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestApplyFrozenPromptInjection_UpstreamModelMismatchIsError(t *testing.T) {
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
		UpstreamModels:    []string{"upstream-a"},
	})

	body := []byte(`{"model":"upstream-b","messages":[{"role":"user","content":"hi"}]}`)
	_, _, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "upstream-b", body)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPromptUpstreamUnsupported))
}

func TestApplyFrozenPromptInjection_ImageTaskIsSkipped(t *testing.T) {
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileResponsesHTTP},
	})

	body := []byte(`{"model":"m","tools":[{"type":"image_generation"}],"instructions":"client","input":[]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileResponsesHTTP, "m", body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, "client", gjson.GetBytes(out, "instructions").String())
}

func TestApplyFrozenPromptInjection_AdapterFailureIsReported(t *testing.T) {
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	// Chat profile 但请求体没有 messages 数组 → 结构不适配。
	_, _, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", []byte(`{"model":"m"}`))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPromptAdapterFailed))
}

func TestPromptPolicySummary_DoesNotLeakBody(t *testing.T) {
	policy := &FrozenPromptPolicy{Enabled: true, VersionID: 7, ManifestSHA256: "abcdef0123456789", BindingSource: PromptBindingSourceGroup, Body: "secret-body"}
	summary := PromptPolicySummary(policy)
	require.NotContains(t, summary, "secret-body")
	require.Contains(t, summary, "version_id=7")
}

// ---------------------------------------------------------------- 运行期记录

type fakePromptEventRecorder struct {
	events []PromptRequestEvent
}

func (f *fakePromptEventRecorder) RecordPromptRequestEvent(_ context.Context, event PromptRequestEvent) {
	f.events = append(f.events, event)
}

func TestApplyFrozenPromptInjection_RecordsAppliedEvent(t *testing.T) {
	recorder := &fakePromptEventRecorder{}
	svc := newPromptTestService(true)
	svc.SetPromptEventRecorder(recorder)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		GroupID:           11,
		AccountID:         22,
		BindingSource:     PromptBindingSourceGroup,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", body)
	require.NoError(t, err)
	require.True(t, changed)

	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	require.True(t, event.Applied)
	require.Equal(t, PromptReasonApplied, event.Reason)
	require.Equal(t, 1, event.AttemptNo)
	require.Equal(t, PromptProfileChatHTTP, event.OutboundProfile)
	require.NotNil(t, event.VersionID)
	require.Equal(t, int64(7), *event.VersionID)
	require.Greater(t, event.AddedBytes, 0)
	// 记录里不得出现提示词正文。
	require.NotContains(t, event.Reason, "server-prompt")
}

func TestApplyFrozenPromptInjection_RecordsSkipReasons(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		body    []byte
		policy  *FrozenPromptPolicy
		reason  string
	}{
		{
			name:    "unsupported profile",
			profile: "",
			body:    []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
			policy:  &FrozenPromptPolicy{Enabled: true, VersionID: 7, Body: "p", SupportedProfiles: []string{PromptProfileChatHTTP}},
			reason:  PromptReasonUnsupportedProfile,
		},
		{
			name:    "non text task",
			profile: PromptProfileResponsesHTTP,
			body:    []byte(`{"model":"m","tools":[{"type":"image_generation"}],"input":[]}`),
			policy:  &FrozenPromptPolicy{Enabled: true, VersionID: 7, Body: "p", SupportedProfiles: []string{PromptProfileResponsesHTTP}},
			reason:  PromptReasonSkippedNonTextTask,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &fakePromptEventRecorder{}
			svc := newPromptTestService(true)
			svc.SetPromptEventRecorder(recorder)
			c := newPromptTestGinContext(t)
			WithFrozenPromptPolicy(c, tc.policy)

			_, changed, err := svc.ApplyFrozenPromptInjection(c, tc.profile, "m", tc.body)
			require.NoError(t, err)
			require.False(t, changed)
			require.Len(t, recorder.events, 1)
			require.False(t, recorder.events[0].Applied)
			require.Equal(t, tc.reason, recorder.events[0].Reason)
			require.Zero(t, recorder.events[0].AddedBytes)
		})
	}
}

func TestApplyFrozenPromptInjection_AttemptNoIncrementsPerAttempt(t *testing.T) {
	// 同一个逻辑请求内每次上游尝试调用一次，序号必须递增，便于后台看出重试次数。
	recorder := &fakePromptEventRecorder{}
	svc := newPromptTestService(true)
	svc.SetPromptEventRecorder(recorder)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	for i := 0; i < 3; i++ {
		_, _, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", body)
		require.NoError(t, err)
	}
	require.Len(t, recorder.events, 3)
	require.Equal(t, []int{1, 2, 3}, []int{recorder.events[0].AttemptNo, recorder.events[1].AttemptNo, recorder.events[2].AttemptNo})
}

func TestApplyFrozenPromptInjection_NoRecorderIsSafe(t *testing.T) {
	// 未注入记录器时不得 panic，注入行为不受影响。
	svc := newPromptTestService(true)
	c := newPromptTestGinContext(t)
	WithFrozenPromptPolicy(c, &FrozenPromptPolicy{
		Enabled:           true,
		VersionID:         7,
		Body:              "server-prompt",
		SupportedProfiles: []string{PromptProfileChatHTTP},
	})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, changed, err := svc.ApplyFrozenPromptInjection(c, PromptProfileChatHTTP, "m", body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "server-prompt", gjson.GetBytes(out, "messages.0.content").String())
}
