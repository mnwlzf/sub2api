package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// BPS 平台的地基契约：独立平台、独立网关、oauth-only、Access Token 必填，
// 且绝不能被既有 OpenAI 链路（兼容集合 / Codex 协议）误认为 OpenAI 账号。
func TestOpenAIBPSPlatformRegistration(t *testing.T) {
	require.Equal(t, "openai_bps", PlatformOpenAIBPS)

	// 独立网关：不能进入 OpenAI 兼容集合，否则 OpenAI 调度/目录会选中 BPS 账号。
	bps := &Account{Platform: PlatformOpenAIBPS, Type: AccountTypeOAuth}
	require.True(t, bps.IsOpenAIBPS())
	require.False(t, bps.IsOpenAI())
	require.False(t, bps.IsOpenAICompatible())
	require.False(t, bps.UsesOpenAICodexProtocol(), "BPS 是 oauth 类型但走独立网关，不能被当成 Codex 协议账号")

	openai := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.True(t, openai.UsesOpenAICodexProtocol())
	require.True(t, openai.IsOpenAICompatible())
}

func TestValidateOpenAIBPSAccountInput(t *testing.T) {
	valid := map[string]any{"access_token": "  token-value  "}

	t.Run("非 BPS 平台直接放行", func(t *testing.T) {
		require.NoError(t, ValidateOpenAIBPSAccountInput(PlatformOpenAI, AccountTypeAPIKey, nil, nil))
	})

	t.Run("类型必须是 oauth", func(t *testing.T) {
		err := ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeAPIKey, valid, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "oauth")
	})

	t.Run("创建必须带 token", func(t *testing.T) {
		err := ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeOAuth, map[string]any{}, nil)
		require.Error(t, err)
	})

	t.Run("创建带 token 通过", func(t *testing.T) {
		require.NoError(t, ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeOAuth, valid, nil))
	})

	t.Run("更新留空保留原 token", func(t *testing.T) {
		existing := map[string]any{"access_token": "old-token"}
		require.NoError(t, ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeOAuth, map[string]any{}, existing))
	})

	t.Run("更新替换新 token", func(t *testing.T) {
		existing := map[string]any{"access_token": "old-token"}
		require.NoError(t, ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeOAuth, valid, existing))
	})

	t.Run("账号 ID 必须是字符串", func(t *testing.T) {
		creds := map[string]any{"access_token": "t", "chatgpt_account_id": 123}
		require.Error(t, ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeOAuth, creds, nil))
	})

	t.Run("账号 ID 不能含空白", func(t *testing.T) {
		creds := map[string]any{"access_token": "t", "chatgpt_account_id": "a b"}
		require.Error(t, ValidateOpenAIBPSAccountInput(PlatformOpenAIBPS, AccountTypeOAuth, creds, nil))
	})
}

func TestOpenAIBPSPlatformDefaults(t *testing.T) {
	// 默认关闭：未配置时必须不启用，避免升级后自动把 BPS 流量接进来。
	require.False(t, OpenAIBPSEnabled())

	models := OpenAIBPSDefaultModels()
	require.Contains(t, models, "gpt-6-astra")
	require.Contains(t, models, "gpt-5.6-sol")

	cfg := OpenAIBPSPlatformConfig()
	require.Positive(t, cfg.ContextWindow)
	require.Positive(t, cfg.CompactThreshold)
	require.Less(t, cfg.CompactThreshold, cfg.ContextWindow)

	require.True(t, IsOpenAIBPSPlatform(PlatformOpenAIBPS))
	require.False(t, IsOpenAIBPSPlatform(PlatformOpenAI))
	require.Equal(t, "https://bps.openai.com/basispoints/api/responses", OpenAIBPSUpstreamURL)
}
