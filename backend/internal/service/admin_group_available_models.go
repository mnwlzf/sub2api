package service

import (
	"context"
	"slices"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// GroupAvailableModels 是若干分组「可用模型」的汇总结果。
//
// 「分组可用模型」定义为该分组内各账号可用的模型并集：账号以 credentials.model_mapping
// 的源模型名（客户端请求名）声明自己可服务的模型，这就是模型广场/渠道定价里的模型名。
type GroupAvailableModels struct {
	// Models 去重并按名称排序后的模型名，可直接写入渠道定价条目。
	Models []string
	// AccountCount 参与统计的账号数（已按平台过滤）。
	AccountCount int
	// UnrestrictedAccounts 未配置模型映射、对任意模型放行的账号数；这类账号不贡献
	// 具体模型名，前端据此提示运营者按需手动补充。
	UnrestrictedAccounts int
}

// maxGroupAvailableModelsGroups 限制一次汇总的分组数量，避免管理端误传超大列表。
const maxGroupAvailableModelsGroups = 200

// GetGroupsAvailableModels 汇总若干分组内账号可用的模型。
//
// 平台口径：
//   - platform 为空时按各自分组的平台过滤；显式传入时统一按该平台过滤
//     （渠道定价页是按平台分段配置的，传当前段平台即可）。
//   - composite 分组接受任意具体平台账号，其余分组只接受同平台账号。
//
// 模型口径：
//   - 账号已配置 model_mapping 时取其源模型名（含通配符的条目跳过，定价条目必须
//     是可计费的具体模型名）。
//   - 未配置映射、或 OpenAI 自动透传且未开启按模型筛选时，账号对任意模型放行，
//     计入 UnrestrictedAccounts，不贡献模型名。
func (s *adminServiceImpl) GetGroupsAvailableModels(ctx context.Context, groupIDs []int64, platform string) (*GroupAvailableModels, error) {
	groupIDs = normalizeGroupIDList(groupIDs)
	if len(groupIDs) == 0 {
		return nil, infraerrors.BadRequest("MISSING_PARAMETER", "group_ids parameter is required").
			WithMetadata(map[string]string{"param": "group_ids"})
	}
	if len(groupIDs) > maxGroupAvailableModelsGroups {
		return nil, infraerrors.BadRequest("TOO_MANY_GROUPS", "too many groups requested").
			WithMetadata(map[string]string{"param": "group_ids"})
	}
	if s.groupRepo == nil || s.accountRepo == nil {
		return nil, infraerrors.InternalServer("GROUP_MODELS_UNAVAILABLE", "group model lookup is not configured")
	}

	requestedPlatform := NormalizeGroupPlatform(platform)

	groups := make([]*Group, 0, len(groupIDs))
	accountsByGroup := make(map[int64][]Account, len(groupIDs))
	for _, groupID := range groupIDs {
		group, err := s.groupRepo.GetByIDLite(ctx, groupID)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
		}
		accounts, err := s.accountRepo.ListByGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
		accountsByGroup[groupID] = accounts
	}

	return collectGroupAvailableModels(groups, accountsByGroup, requestedPlatform), nil
}

// collectGroupAvailableModels 是不依赖仓储的汇总实现：按分组平台（或显式平台）过滤账号，
// 收集账号 model_mapping 的源模型名并去重排序。
func collectGroupAvailableModels(groups []*Group, accountsByGroup map[int64][]Account, requestedPlatform string) *GroupAvailableModels {
	result := &GroupAvailableModels{Models: []string{}}
	seen := make(map[string]struct{})

	for _, group := range groups {
		if group == nil {
			continue
		}
		effectivePlatform := requestedPlatform
		if effectivePlatform == "" {
			effectivePlatform = NormalizeGroupPlatform(group.Platform)
		}
		accounts := accountsByGroup[group.ID]
		for i := range accounts {
			account := &accounts[i]
			if !accountMatchesAvailableModelPlatform(account, effectivePlatform) {
				continue
			}
			result.AccountCount++
			if !accountEnumeratesModels(account) {
				result.UnrestrictedAccounts++
				continue
			}
			for model := range account.GetModelMapping() {
				model = strings.TrimSpace(model)
				if model == "" || strings.ContainsAny(model, "*?") {
					continue
				}
				if _, ok := seen[model]; ok {
					continue
				}
				seen[model] = struct{}{}
				result.Models = append(result.Models, model)
			}
		}
	}

	slices.SortStableFunc(result.Models, func(a, b string) int {
		if cmp := strings.Compare(strings.ToLower(a), strings.ToLower(b)); cmp != 0 {
			return cmp
		}
		return strings.Compare(a, b)
	})
	return result
}

// accountMatchesAvailableModelPlatform 判断账号是否参与该平台下的模型汇总。
func accountMatchesAvailableModelPlatform(account *Account, platform string) bool {
	if account == nil {
		return false
	}
	if platform == PlatformComposite {
		return isConcreteRequestPlatform(account.Platform)
	}
	if platform == "" {
		return isConcreteRequestPlatform(account.Platform)
	}
	return account.Platform == platform
}

// accountEnumeratesModels 判断账号是否能用 model_mapping 枚举出其可用模型。
// 返回 false 表示账号对任意模型放行（无映射或自动透传未开启按模型筛选）。
func accountEnumeratesModels(account *Account) bool {
	if account == nil {
		return false
	}
	if account.IsOpenAIPassthroughEnabled() && !account.IsOpenAIPassthroughModelFilterEnabled() {
		return false
	}
	return len(account.GetModelMapping()) > 0
}

// normalizeGroupIDList 去重并丢弃非正数分组 ID，保持传入顺序。
func normalizeGroupIDList(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
