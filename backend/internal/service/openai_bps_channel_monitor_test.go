package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// 渠道监测（BPS 阶段 7）现状锁定：
//   - 用量维度（channel_monitor_v2）的平台是 usage/error 事实里的自由字符串，
//     只要 BPS 用量行写入后就会自动出现；平台登记在 domain 清单里（不是
//     channel_monitor 自己的白名单），这里证明登记与配置归一都接受 openai_bps。
//   - 探活模板的 provider 是显式清单（与迁移 CHECK 约束一致），BPS 尚未注册；
//     现有 openai:responses 适配器固定拼 /v1/responses，无法打到 BPS 的固定上游
//     /basispoints/api/responses，因此必须新增 provider/template 接线（见汇报）。
func TestOpenAIBPSRegisteredAsConcretePlatform(t *testing.T) {
	spec, ok := domain.LookupPlatform(PlatformOpenAIBPS)
	require.True(t, ok, "BPS 必须登记在 domain 平台清单")
	require.Equal(t, domain.PlatformGatewayOpenAIBPS, spec.Gateway)
	require.Equal(t, "OpenAI BPS", spec.DisplayName)
	require.Contains(t, domain.ConcretePlatformIDs(), PlatformOpenAIBPS)
}

func TestOpenAIBPSChannelMonitorV2AcceptsPlatform(t *testing.T) {
	cfg := ChannelMonitorV2Config{
		Version:                1,
		Enabled:                true,
		RefreshIntervalSeconds: 60,
		Platforms: []ChannelMonitorV2PlatformConfig{
			{Platform: PlatformOpenAIBPS, Enabled: true, Models: []string{"gpt-6-astra"}},
		},
	}
	require.NoError(t, normalizeChannelMonitorV2Config(&cfg))
	require.Len(t, cfg.Platforms, 1)
	require.Equal(t, PlatformOpenAIBPS, cfg.Platforms[0].Platform)
	require.Equal(t, []string{"gpt-6-astra"}, cfg.Platforms[0].Models)
}

func TestOpenAIBPSProbeTemplateRequiresExplicitWiring(t *testing.T) {
	// provider 清单是显式表（注释：与迁移 226 的 CHECK 约束一致），不是 domain
	// 派生；BPS 目前不在其中，因此建不了 BPS 探活模板。
	_, registered := monitorProviders[PlatformOpenAIBPS]
	require.False(t, registered, "若 BPS provider 已接线，请更新本测试与模板接线说明")

	// 现有 OpenAI Responses 适配器固定拼 /v1/responses；BPS 固定上游路径是
	// /basispoints/api/responses，joinURL 不会覆盖该前缀，证明不能直接复用。
	adapter, apiMode, ok := providerAdapterFor(MonitorProviderOpenAI, MonitorAPIModeResponses)
	require.True(t, ok)
	require.Equal(t, MonitorAPIModeResponses, apiMode)
	require.Equal(t, "/v1/responses", adapter.buildPath(""))
	require.NotEqual(t, OpenAIBPSUpstreamEndpointPath, adapter.buildPath(""),
		"BPS 上游路径与 openai:responses 模板不同，必须新增 BPS 模板")
}
