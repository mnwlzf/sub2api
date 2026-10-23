package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// bpsCatalogAccount 构造一个可用的 BPS 账号；mapping 为 nil 表示未配置
// model_mapping（白名单语义 = 允许全部）。
func bpsCatalogAccount(id int64, mapping map[string]any) Account {
	credentials := map[string]any{"access_token": "token"}
	if mapping != nil {
		credentials["model_mapping"] = mapping
	}
	return Account{
		ID:          id,
		Platform:    PlatformOpenAIBPS,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: credentials,
	}
}

func decodeOpenAIBPSManifest(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var manifest struct {
		Models []map[string]any `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &manifest))
	require.NotNil(t, manifest.Models)
	return manifest.Models
}

func openAIBPSManifestSlugs(t *testing.T, body []byte) []string {
	t.Helper()
	models := decodeOpenAIBPSManifest(t, body)
	slugs := make([]string, 0, len(models))
	for _, model := range models {
		slug, ok := model["slug"].(string)
		require.True(t, ok)
		slugs = append(slugs, slug)
	}
	return slugs
}

func TestOpenAIBPSCatalogModelCapabilities(t *testing.T) {
	cfg := OpenAIBPSPlatformConfig()
	model := OpenAIBPSCatalogModelForID("  gpt-6-astra  ")

	require.Equal(t, "gpt-6-astra", model.ID)
	require.Equal(t, "gpt-6-astra", model.DisplayName)
	require.Equal(t, cfg.ContextWindow, model.ContextWindow)
	require.Equal(t, cfg.CompactThreshold, model.CompactThreshold)
	require.Less(t, model.CompactThreshold, model.ContextWindow)

	// 能力声明：串行工具 + 文本 + HTTP，不含 WebSocket。
	require.True(t, model.SupportsToolCalls)
	require.False(t, model.SupportsParallelToolCalls)
	require.Equal(t, []string{"text"}, model.InputModalities)
	require.True(t, model.SupportsHTTPResponses)
	require.False(t, model.SupportsWebSocketResponses)
	require.False(t, model.SupportsSearchTool)
	require.False(t, model.SupportsReasoningSummaryParameter)
	require.False(t, model.SupportsVerbosity)

	require.Equal(t, "medium", model.DefaultReasoningLevel)
	require.Equal(t, []string{"low", "medium", "high", "xhigh"}, model.SupportedReasoningLevels)
}

func TestBuildOpenAIBPSCodexModelsManifestShape(t *testing.T) {
	cfg := OpenAIBPSPlatformConfig()
	body, err := BuildOpenAIBPSCodexModelsManifest([]string{"gpt-6-astra", "gpt-5.6-sol"})
	require.NoError(t, err)
	models := decodeOpenAIBPSManifest(t, body)
	require.Len(t, models, 2)

	entry := models[0]
	require.Equal(t, "gpt-6-astra", entry["slug"])
	require.Equal(t, "gpt-6-astra", entry["display_name"])
	require.Equal(t, "text", entry["web_search_tool_type"])
	require.Equal(t, []any{"text"}, entry["input_modalities"])
	require.Equal(t, false, entry["supports_parallel_tool_calls"])
	require.Equal(t, false, entry["prefer_websockets"])
	require.Equal(t, false, entry["supports_reasoning_summary_parameter"])
	require.Equal(t, false, entry["support_verbosity"])
	require.Equal(t, false, entry["supports_search_tool"])
	require.Equal(t, false, entry["supports_image_detail_original"])
	require.Equal(t, "medium", entry["default_reasoning_level"])
	require.Equal(t, "medium", entry["multi_agent_reasoning_effort"])
	require.EqualValues(t, cfg.ContextWindow, entry["context_window"])
	require.EqualValues(t, cfg.ContextWindow, entry["max_context_window"])
	require.EqualValues(t, cfg.CompactThreshold, entry["auto_compact_token_limit"])
	require.Equal(t, []any{}, entry["additional_speed_tiers"])
	require.Equal(t, []any{}, entry["service_tiers"])
	value, ok := entry["default_service_tier"]
	require.True(t, ok, "default_service_tier 必须显式输出 null")
	require.Nil(t, value)

	levels, ok := entry["supported_reasoning_levels"].([]any)
	require.True(t, ok)
	require.Len(t, levels, 4)
	for index, effort := range []string{"low", "medium", "high", "xhigh"} {
		level, isObject := levels[index].(map[string]any)
		require.True(t, isObject)
		require.Equal(t, effort, level["effort"])
		require.NotEmpty(t, level["description"])
	}

	// 与 BuildCodexModelsManifest 同形状：既有 manifest 的字段全部保留，
	// BPS 只额外补 prefer_websockets。
	baseBody, err := BuildCodexModelsManifest([]string{"gpt-6-astra"})
	require.NoError(t, err)
	base := decodeOpenAIBPSManifest(t, baseBody)
	require.Len(t, base, 1)
	for key := range base[0] {
		require.Contains(t, entry, key, "BPS manifest 缺少既有 manifest 字段 %q", key)
	}
	require.Contains(t, entry, "prefer_websockets")
}

func TestBuildOpenAIBPSCodexModelsManifestEmptyAndFiltering(t *testing.T) {
	body, err := BuildOpenAIBPSCodexModelsManifest(nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[]}`, string(body))

	body, err = BuildOpenAIBPSCodexModelsManifest([]string{"  ", "gpt-6-astra", " gpt-6-astra ", "gpt-image-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-6-astra"}, openAIBPSManifestSlugs(t, body))
}

func TestOpenAIBPSModelIDsForGroup(t *testing.T) {
	t.Run("mapping aliases plus defaults for unmapped accounts", func(t *testing.T) {
		accounts := []Account{
			bpsCatalogAccount(1, map[string]any{"bps-alias": "gpt-6-astra", "shared": "gpt-5.6-sol"}),
			bpsCatalogAccount(2, nil),
		}
		require.Equal(t,
			[]string{"bps-alias", "gpt-5.6-sol", "gpt-6-astra", "shared"},
			OpenAIBPSModelIDsForGroup(nil, accounts),
		)
	})

	t.Run("mapped-only account does not advertise default models", func(t *testing.T) {
		accounts := []Account{bpsCatalogAccount(1, map[string]any{"bps-alias": "gpt-6-astra"})}
		require.Equal(t, []string{"bps-alias"}, OpenAIBPSModelIDsForGroup(nil, accounts))
	})

	t.Run("mapping target is published when the account whitelist accepts it", func(t *testing.T) {
		// 通配符键覆盖目标名，目标才是客户端可请求的公开名；通配符键本身不是模型名。
		accounts := []Account{bpsCatalogAccount(1, map[string]any{"gpt-5*": "gpt-5.6-sol"})}
		require.Equal(t, []string{"gpt-5.6-sol"}, OpenAIBPSModelIDsForGroup(nil, accounts))
	})

	t.Run("non-BPS accounts are excluded", func(t *testing.T) {
		accounts := []Account{{
			ID:          9,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"openai-only": "gpt-5"}},
		}}
		require.Empty(t, OpenAIBPSModelIDsForGroup(nil, accounts))
	})

	t.Run("dedupe and stable sort", func(t *testing.T) {
		accounts := []Account{
			bpsCatalogAccount(1, map[string]any{"zeta": "gpt-6-astra", "alpha": "gpt-6-astra"}),
			bpsCatalogAccount(2, map[string]any{"alpha": "gpt-5.6-sol", "beta": "gpt-5.6-sol"}),
		}
		got := OpenAIBPSModelIDsForGroup(nil, accounts)
		require.Equal(t, []string{"alpha", "beta", "zeta"}, got)
		require.Equal(t, got, OpenAIBPSModelIDsForGroup(nil, accounts), "同一输入必须产生稳定顺序")
	})

	t.Run("group allowlist narrows the catalog", func(t *testing.T) {
		accounts := []Account{
			bpsCatalogAccount(1, map[string]any{"zeta": "gpt-6-astra", "alpha": "gpt-6-astra"}),
		}
		group := &Group{
			Platform:       PlatformOpenAIBPS,
			ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"alpha", "zeta"}},
		}
		require.Equal(t, []string{"alpha", "zeta"}, OpenAIBPSModelIDsForGroup(group, accounts))
	})

	t.Run("media models are not advertised", func(t *testing.T) {
		accounts := []Account{bpsCatalogAccount(1, map[string]any{"gpt-image-1": "gpt-image-1"})}
		require.Empty(t, OpenAIBPSModelIDsForGroup(nil, accounts))
	})
}

func TestBuildOpenAIBPSGroupCodexModelsManifest(t *testing.T) {
	group := &Group{Platform: PlatformOpenAIBPS}
	accounts := []Account{
		bpsCatalogAccount(1, map[string]any{"bps-alias": "gpt-6-astra"}),
		bpsCatalogAccount(2, nil),
	}

	body, err := BuildOpenAIBPSGroupCodexModelsManifest(group, accounts)
	require.NoError(t, err)
	require.Equal(t, OpenAIBPSModelIDsForGroup(group, accounts), openAIBPSManifestSlugs(t, body))

	emptyBody, err := BuildOpenAIBPSGroupCodexModelsManifest(group, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[]}`, string(emptyBody))
}
