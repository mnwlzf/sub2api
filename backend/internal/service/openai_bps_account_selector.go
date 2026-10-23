package service

import (
	"context"
	"net/http"
	"sort"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"github.com/gin-gonic/gin"
)

// OpenAIBPSAccountSelector 是 BPS 网关的选号器，实现 handler 的三个可选接口：
//
//   - OpenAIBPSAccountResolver：未绑定会话按分组 + 平台 + 可调度性选号；
//   - OpenAIBPSBoundAccountLookup：已绑定会话按 ID 精确取号（硬约束，不重调度）；
//   - OpenAIBPSAccountReleaser：阶段 3 未占用并发槽，释放为空操作。
//
// 阶段 3 使用确定性选择（分组内可调度账号按优先级、ID 排序），完整的负载/并发
// 调度留待后续阶段接入。BPS 平台不在 OpenAI 兼容集合内，因此不能复用
// OpenAIGatewayService.SelectAccountForModel（它会把平台归一成 openai）。
// OpenAIBPSAccountSource 是选号器所需的账号仓储窄接口（AccountRepository 自动满足）。
type OpenAIBPSAccountSource interface {
	ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error)
	GetByID(ctx context.Context, id int64) (*Account, error)
}

type OpenAIBPSAccountSelector struct {
	accounts OpenAIBPSAccountSource
}

var (
	_ OpenAIBPSAccountResolver    = (*OpenAIBPSAccountSelector)(nil)
	_ OpenAIBPSBoundAccountLookup = (*OpenAIBPSAccountSelector)(nil)
	_ OpenAIBPSAccountReleaser    = (*OpenAIBPSAccountSelector)(nil)
)

// NewOpenAIBPSAccountSelector 构造 BPS 选号器。
func NewOpenAIBPSAccountSelector(accounts OpenAIBPSAccountSource) *OpenAIBPSAccountSelector {
	return &OpenAIBPSAccountSelector{accounts: accounts}
}

// SelectOpenAIBPSAccount 从 API Key 所属分组中选择一个可用的 BPS 账号。
func (s *OpenAIBPSAccountSelector) SelectOpenAIBPSAccount(ctx context.Context, c *gin.Context, model string) (*Account, error) {
	if s == nil || s.accounts == nil {
		return nil, infraerrors.Newf(http.StatusServiceUnavailable, "bps_state_unavailable",
			"OpenAI BPS 选号器未初始化")
	}
	apiKey := getAPIKeyFromContext(c)
	if apiKey == nil || apiKey.GroupID == nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, "bps_group_required",
			"OpenAI BPS 请求必须绑定分组")
	}
	accounts, err := s.accounts.ListSchedulableByGroupIDAndPlatform(ctx, *apiKey.GroupID, PlatformOpenAIBPS)
	if err != nil {
		return nil, err
	}
	candidates := make([]*Account, 0, len(accounts))
	for i := range accounts {
		if openAIBPSAccountSelectable(&accounts[i], model) {
			candidates = append(candidates, &accounts[i])
		}
	}
	if len(candidates) == 0 {
		return nil, infraerrors.Newf(http.StatusConflict, "bps_account_unavailable",
			"没有可用的 OpenAI BPS 账号")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], nil
}

// OpenAIBPSAccountByID 精确取回已绑定会话的账号（不重调度）。
func (s *OpenAIBPSAccountSelector) OpenAIBPSAccountByID(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accounts == nil {
		return nil, infraerrors.Newf(http.StatusServiceUnavailable, "bps_state_unavailable",
			"OpenAI BPS 选号器未初始化")
	}
	return s.accounts.GetByID(ctx, accountID)
}

// ReleaseOpenAIBPSAccount 在阶段 3 为空操作（未占用并发槽）。
func (s *OpenAIBPSAccountSelector) ReleaseOpenAIBPSAccount(_ context.Context, _ *Account) {}

// openAIBPSAccountSelectable 判断账号是否可作为候选：
// 平台匹配、可调度、模型支持、凭证状态不是过期/撤销/认证失败。
func openAIBPSAccountSelectable(account *Account, model string) bool {
	if account == nil || !account.IsOpenAIBPS() || !account.IsSchedulable() {
		return false
	}
	if !account.IsModelSupported(model) {
		return false
	}
	switch ResolveOpenAIBPSCredentialState(account, time.Now()).Status {
	case OpenAIBPSCredentialStatusExpired, OpenAIBPSCredentialStatusRevoked, OpenAIBPSCredentialStatusAuthFailed:
		return false
	}
	return true
}
