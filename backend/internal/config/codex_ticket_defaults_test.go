package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCodexTicketDefaults 验证 init() 注册的默认值：全局默认关闭、292 长度、
// 与参考实现对齐的模型名单与节奏。
func TestCodexTicketDefaults(t *testing.T) {
	// 不重置全局 viper：其它测试可能已 Reset，这里幂等重注册即可，避免反过来污染后续测试。
	RegisterOpenAICodexTicketDefaults()

	cfg := (&Config{}).CodexTicket()
	require.False(t, cfg.Enabled, "打票必须全局默认关闭")
	require.Equal(t, DefaultOpenAICodexTicketTargetLength, cfg.TargetLength)
	require.Equal(t, DefaultOpenAICodexTicketTTLSeconds, cfg.TTLSeconds)
	require.Equal(t, DefaultOpenAICodexTicketRefreshBeforeSeconds, cfg.RefreshBeforeSeconds)
	require.Equal(t, DefaultOpenAICodexTicketProbeIntervalSeconds, cfg.HarvestProbeIntervalSeconds)
	require.Equal(t, DefaultOpenAICodexTicketAttemptTimeoutSeconds, cfg.HarvestAttemptTimeoutSeconds)
	require.False(t, cfg.FailClosed)
	require.Equal(t, DefaultOpenAICodexTicketModels, cfg.Models)
	require.Equal(t, DefaultOpenAICodexTicketRetryMinSeconds, cfg.HarvestRetryMinSeconds)
	require.Equal(t, DefaultOpenAICodexTicketRetryMaxSeconds, cfg.HarvestRetryMaxSeconds)
	require.Equal(t, DefaultOpenAICodexTicketRefreshSeconds, cfg.HarvestRefreshSeconds)
}

// TestNormalizeCodexTicketConfig 验证越界值归一化与模型名单去重。
func TestNormalizeCodexTicketConfig(t *testing.T) {
	cfg := NormalizeCodexTicketConfig(CodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 -1,
		TTLSeconds:                   0,
		RefreshBeforeSeconds:         -5,
		HarvestProbeIntervalSeconds:  -1,
		HarvestAttemptTimeoutSeconds: -1,
		HarvestRetryMinSeconds:       30,
		HarvestRetryMaxSeconds:       10, // max < min → 抬到 min
		HarvestRefreshSeconds:        -1,
		Models:                       []string{" gpt-5.6-sol ", "", "gpt-5.6-sol", "gpt-6-astra"},
	})
	require.True(t, cfg.Enabled)
	require.Equal(t, DefaultOpenAICodexTicketTargetLength, cfg.TargetLength)
	require.Equal(t, DefaultOpenAICodexTicketTTLSeconds, cfg.TTLSeconds)
	require.Equal(t, DefaultOpenAICodexTicketRefreshBeforeSeconds, cfg.RefreshBeforeSeconds)
	require.Equal(t, DefaultOpenAICodexTicketProbeIntervalSeconds, cfg.HarvestProbeIntervalSeconds)
	require.Equal(t, DefaultOpenAICodexTicketAttemptTimeoutSeconds, cfg.HarvestAttemptTimeoutSeconds)
	require.Equal(t, 30, cfg.HarvestRetryMinSeconds)
	require.Equal(t, 30, cfg.HarvestRetryMaxSeconds)
	require.Equal(t, DefaultOpenAICodexTicketRefreshSeconds, cfg.HarvestRefreshSeconds)
	require.Equal(t, []string{"gpt-5.6-sol", "gpt-6-astra"}, cfg.Models)
}

// TestCodexTicketDefaultsRegisteredBeforeLoad 验证未调用 Load 时（单测/降级路径）
// 也能从全局 viper 拿到默认值——CodexTicket() 必须对 nil Config 也安全。
func TestCodexTicketDefaultsRegisteredBeforeLoad(t *testing.T) {
	RegisterOpenAICodexTicketDefaults()

	var nilConfig *Config
	cfg := nilConfig.CodexTicket()
	require.False(t, cfg.Enabled)
	require.Equal(t, DefaultOpenAICodexTicketTargetLength, cfg.TargetLength)
	require.NotEmpty(t, cfg.Models)
}
