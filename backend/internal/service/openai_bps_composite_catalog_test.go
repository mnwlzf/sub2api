package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func openAIBPSCompositeEntryIDs(entries []OpenAIBPSCatalogModel) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func TestBuildOpenAIBPSCompositeCatalogEntries(t *testing.T) {
	unmapped := bpsCatalogAccount(1, nil)
	mapped := bpsCatalogAccount(2, map[string]any{"team-a": "team-a"})
	openAIAccount := Account{
		ID:          9,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
	}

	t.Run("exact route publishes the alias directly", func(t *testing.T) {
		routes := []CompositeModelRoute{{
			ID: 1, PublicModel: "bps-alias", MatchType: CompositeRouteMatchExact,
			TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "gpt-6-astra",
			Endpoint: CompositeRouteEndpointResponses, Enabled: true,
		}}
		entries := BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{unmapped})
		require.Equal(t, []string{"bps-alias"}, openAIBPSCompositeEntryIDs(entries))
		require.True(t, entries[0].SupportsToolCalls)
		require.False(t, entries[0].SupportsParallelToolCalls)
		require.Equal(t, []string{"text"}, entries[0].InputModalities)
		require.True(t, entries[0].SupportsHTTPResponses)
		require.False(t, entries[0].SupportsWebSocketResponses)
	})

	t.Run("exact route requires a BPS account accepting the upstream model", func(t *testing.T) {
		routes := []CompositeModelRoute{{
			ID: 1, PublicModel: "bps-alias", MatchType: CompositeRouteMatchExact,
			TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "gpt-6-astra", Enabled: true,
		}}
		// 映射账号只接受 team-a：改写后的上游模型 gpt-6-astra 会被选号器拒绝。
		require.Empty(t, BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{mapped}))
		require.Empty(t, BuildOpenAIBPSCompositeCatalogEntries(routes, nil))
		require.Empty(t, BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{openAIAccount}))

		// 上游模型在账号白名单内时才发布。
		routes[0].UpstreamModel = "team-a"
		require.Equal(t, []string{"bps-alias"}, openAIBPSCompositeEntryIDs(
			BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{mapped}),
		))
	})

	t.Run("prefix route filters enumerable account models", func(t *testing.T) {
		routes := []CompositeModelRoute{{
			ID: 1, PublicModel: "gpt-", MatchType: CompositeRouteMatchPrefix,
			TargetPlatform: PlatformOpenAIBPS, Endpoint: CompositeRouteEndpointAny, Enabled: true,
		}}
		// 未映射账号可枚举平台默认模型，前缀命中 gpt-*。
		require.Equal(t, []string{"gpt-5.6-sol", "gpt-6-astra"}, openAIBPSCompositeEntryIDs(
			BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{unmapped}),
		))

		// 映射账号只暴露 team-a，不会因为前缀是 gpt- 就凭空生成模型。
		require.Empty(t, BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{mapped}))
	})

	t.Run("prefix route with fixed upstream gates on the upstream model", func(t *testing.T) {
		routes := []CompositeModelRoute{{
			ID: 1, PublicModel: "team-", MatchType: CompositeRouteMatchPrefix,
			TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "gpt-6-astra", Enabled: true,
		}}
		// team-a 可枚举，但固定上游 gpt-6-astra 不被映射账号接受 → 不发布。
		require.Empty(t, BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{mapped}))
		// 有未映射账号兜底上游模型后，team-a 才能发布。
		require.Equal(t, []string{"team-a"}, openAIBPSCompositeEntryIDs(
			BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{mapped, unmapped}),
		))
	})

	t.Run("no candidates returns an empty list without cross-platform fallback", func(t *testing.T) {
		// 指向其它平台的路由不是 BPS 候选；gpt-6-astra 的检测器结果是 openai，
		// 也绝不能因此回退到 OpenAI 目录。
		routes := []CompositeModelRoute{{
			ID: 1, PublicModel: "gpt-6-astra", MatchType: CompositeRouteMatchExact,
			TargetPlatform: PlatformOpenAI, UpstreamModel: "gpt-6-astra", Enabled: true,
		}}
		entries := BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{unmapped})
		require.NotNil(t, entries, "无候选也必须返回空列表而不是 nil")
		require.Empty(t, entries)

		routes = []CompositeModelRoute{
			{ID: 1, PublicModel: "disabled", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, Enabled: false},
			{ID: 2, PublicModel: "chat-only", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, Endpoint: CompositeRouteEndpointChatCompletions, Enabled: true},
			{ID: 3, PublicModel: "   ", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, Enabled: true},
		}
		require.Empty(t, BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{unmapped}))
	})

	t.Run("deduplicates aliases and sorts entries", func(t *testing.T) {
		routes := []CompositeModelRoute{
			{ID: 2, PublicModel: "zeta", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "zeta", Enabled: true},
			{ID: 1, PublicModel: "alpha", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "alpha", Enabled: true},
			{ID: 3, PublicModel: "alpha", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "alpha", Enabled: true},
		}
		entries := BuildOpenAIBPSCompositeCatalogEntries(routes, []Account{unmapped})
		require.Equal(t, []string{"alpha", "zeta"}, openAIBPSCompositeEntryIDs(entries))
		require.Equal(t, "alpha", entries[0].DisplayName)
		require.Equal(t, OpenAIBPSPlatformConfig().ContextWindow, entries[0].ContextWindow)
	})
}

// Composite 路由解析对 target_platform=openai_bps 无需新增分支：解析器直接
// 透传 route.TargetPlatform，平台校验走 domain 注册表（openai_bps 已登记）。
// 本测试固定这一契约，防止后续把 BPS 排除出具体平台集合。
func TestOpenAIBPSCompositeRouteResolutionIsExplicit(t *testing.T) {
	// BPS 不是检测器推导结果：gpt-* 名字属于 OpenAI，BPS 必须显式路由。
	detectedPlatform, detected := DetectModelPlatform("gpt-6-astra")
	require.True(t, detected)
	require.Equal(t, PlatformOpenAI, detectedPlatform)
	require.True(t, isConcreteRequestPlatform(PlatformOpenAIBPS))

	route, err := compositeRouteFromInput(7, CompositeRouteInput{
		PublicModel:    " bps-alias ",
		MatchType:      CompositeRouteMatchExact,
		TargetPlatform: PlatformOpenAIBPS,
		Endpoint:       CompositeRouteEndpointResponses,
		Enabled:        true,
	})
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAIBPS, route.TargetPlatform)
	require.Equal(t, "bps-alias", route.PublicModel)
	require.Equal(t, "bps-alias", route.UpstreamModel, "exact 路由缺省 upstream 回填 public_model")

	resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: []CompositeModelRoute{
		{ID: 1, GroupID: 7, PublicModel: "bps-alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAIBPS, UpstreamModel: "gpt-6-astra", Endpoint: CompositeRouteEndpointResponses, Enabled: true},
		{ID: 2, GroupID: 7, PublicModel: "bps-prefix", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformOpenAIBPS, Enabled: true},
	}})

	decision, err := resolver.Resolve(context.Background(), 7, "bps-alias", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, CompositeRouteSourceExplicit, decision.Source)
	require.Equal(t, PlatformOpenAIBPS, decision.TargetPlatform)
	require.Equal(t, "gpt-6-astra", decision.UpstreamModel)

	prefixDecision, err := resolver.Resolve(context.Background(), 7, "bps-prefix-1", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.True(t, prefixDecision.Matched)
	require.Equal(t, PlatformOpenAIBPS, prefixDecision.TargetPlatform)
	require.Equal(t, "bps-prefix-1", prefixDecision.UpstreamModel, "prefix 路由留空 upstream 时透传请求模型")

	ctx := WithCompositeRouteDecision(context.Background(), decision)
	resolvedPlatform, ok := ResolvedTargetPlatformFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, PlatformOpenAIBPS, resolvedPlatform)
	resolvedModel, ok := ResolvedUpstreamModelFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "gpt-6-astra", resolvedModel)
}
