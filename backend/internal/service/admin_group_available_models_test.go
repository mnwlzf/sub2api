package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func availableModelTestAccount(id int64, platform string, mapping map[string]any, extra map[string]any) Account {
	credentials := map[string]any{"api_key": "k"}
	if mapping != nil {
		credentials["model_mapping"] = mapping
	}
	account := Account{ID: id, Platform: platform, Type: AccountTypeAPIKey, Credentials: credentials}
	account.Extra = map[string]any{}
	for k, v := range extra {
		account.Extra[k] = v
	}
	return account
}

func TestCollectGroupAvailableModelsUnionsMappings(t *testing.T) {
	groups := []*Group{
		{ID: 1, Platform: PlatformOpenAI},
		{ID: 2, Platform: PlatformOpenAI},
	}
	accounts := map[int64][]Account{
		1: {
			availableModelTestAccount(11, PlatformOpenAI, map[string]any{"gpt-5.5": "gpt-5.5", "z-model": "z-model"}, nil),
			availableModelTestAccount(12, PlatformOpenAI, map[string]any{"gpt-5.5": "gpt-5.5", "a-model": "a-model"}, nil),
			// 其他平台账号不参与 openai 汇总。
			availableModelTestAccount(13, PlatformAnthropic, map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}, nil),
		},
		2: {
			availableModelTestAccount(21, PlatformOpenAI, map[string]any{"b-model": "b-model"}, nil),
		},
	}

	got := collectGroupAvailableModels(groups, accounts, "")
	require.Equal(t, []string{"a-model", "b-model", "gpt-5.5", "z-model"}, got.Models)
	require.Equal(t, 3, got.AccountCount)
	require.Zero(t, got.UnrestrictedAccounts)
}

func TestCollectGroupAvailableModelsSkipsWildcardsAndDuplicates(t *testing.T) {
	groups := []*Group{{ID: 1, Platform: PlatformOpenAI}}
	accounts := map[int64][]Account{
		1: {
			availableModelTestAccount(11, PlatformOpenAI, map[string]any{
				"gpt-*":   "gpt-5.5",
				"glm-5?":  "glm-5.3",
				"  ":      "empty",
				"gpt-6.2": "gpt-6.2-sol",
			}, nil),
			availableModelTestAccount(12, PlatformOpenAI, map[string]any{"gpt-6.2": "gpt-6.2-sol"}, nil),
		},
	}

	got := collectGroupAvailableModels(groups, accounts, "")
	require.Equal(t, []string{"gpt-6.2"}, got.Models)
	require.Equal(t, 2, got.AccountCount)
}

func TestCollectGroupAvailableModelsCountsUnrestrictedAccounts(t *testing.T) {
	groups := []*Group{{ID: 1, Platform: PlatformOpenAI}}
	accounts := map[int64][]Account{
		1: {
			// 无映射：对任意模型放行。
			availableModelTestAccount(11, PlatformOpenAI, nil, nil),
			// 自动透传且未开启按模型筛选：同样对任意模型放行，残留映射不生效。
			availableModelTestAccount(12, PlatformOpenAI, map[string]any{"gpt-5.5": "gpt-5.5"}, map[string]any{
				"openai_passthrough": true,
			}),
			// 自动透传 + 按模型筛选：映射即白名单，正常参与汇总。
			availableModelTestAccount(13, PlatformOpenAI, map[string]any{"gpt-6.2-sol": "gpt-6.2-sol"}, map[string]any{
				"openai_passthrough":              true,
				"openai_passthrough_model_filter": true,
			}),
		},
	}

	got := collectGroupAvailableModels(groups, accounts, "")
	require.Equal(t, []string{"gpt-6.2-sol"}, got.Models)
	require.Equal(t, 3, got.AccountCount)
	require.Equal(t, 2, got.UnrestrictedAccounts)
}

func TestCollectGroupAvailableModelsCompositeAcceptsConcretePlatforms(t *testing.T) {
	groups := []*Group{{ID: 1, Platform: PlatformComposite}}
	accounts := map[int64][]Account{
		1: {
			availableModelTestAccount(11, PlatformOpenAI, map[string]any{"gpt-5.5": "gpt-5.5"}, nil),
			availableModelTestAccount(12, PlatformAnthropic, map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}, nil),
			availableModelTestAccount(13, PlatformComposite, map[string]any{"ignored": "ignored"}, nil),
		},
	}

	got := collectGroupAvailableModels(groups, accounts, "")
	require.Equal(t, []string{"claude-sonnet-4-6", "gpt-5.5"}, got.Models)
	require.Equal(t, 2, got.AccountCount)
}

func TestCollectGroupAvailableModelsExplicitPlatformFiltersAccounts(t *testing.T) {
	groups := []*Group{{ID: 1, Platform: PlatformComposite}}
	accounts := map[int64][]Account{
		1: {
			availableModelTestAccount(11, PlatformOpenAI, map[string]any{"gpt-5.5": "gpt-5.5"}, nil),
			availableModelTestAccount(12, PlatformAnthropic, map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}, nil),
		},
	}

	got := collectGroupAvailableModels(groups, accounts, PlatformOpenAI)
	require.Equal(t, []string{"gpt-5.5"}, got.Models)
	require.Equal(t, 1, got.AccountCount)
}

func TestNormalizeGroupIDListDropsDuplicatesAndInvalidIDs(t *testing.T) {
	require.Equal(t, []int64{3, 1}, normalizeGroupIDList([]int64{0, 3, -1, 3, 1}))
	require.Empty(t, normalizeGroupIDList(nil))
}

func TestGetGroupsAvailableModelsRequiresGroupIDs(t *testing.T) {
	svc := &adminServiceImpl{}
	_, err := svc.GetGroupsAvailableModels(t.Context(), nil, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "group_ids")
}
