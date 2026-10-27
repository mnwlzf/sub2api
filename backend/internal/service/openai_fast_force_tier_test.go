package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// forceFastContext returns a request context whose trusted Group forces OpenAI
// fast, matching what the gateway handlers put on the context.
func forceFastContext(force bool) context.Context {
	group := &Group{
		ID:              66,
		Name:            "normal-fast",
		Platform:        PlatformOpenAI,
		Status:          "active",
		Hydrated:        true,
		ForceOpenAIFast: force,
	}
	return context.WithValue(context.Background(), ctxkey.Group, group)
}

// forcedTierContext returns a context whose group declares an explicit forced
// service tier (the newer, more specific setting).
func forcedTierContext(tier string, force bool) context.Context {
	group := &Group{
		ID:                66,
		Name:              "normal-fast",
		Platform:          PlatformOpenAI,
		Status:            "active",
		Hydrated:          true,
		ForceOpenAIFast:   force,
		ForcedServiceTier: tier,
	}
	return context.WithValue(context.Background(), ctxkey.Group, group)
}

func TestOpenAIGroupForcedServiceTier(t *testing.T) {
	openAIAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	t.Run("no group force leaves the caller tier untouched", func(t *testing.T) {
		for _, current := range []string{"", "priority", "ultrafast", "flex", "default"} {
			tier, ok := openAIGroupForcedServiceTier(forceFastContext(false), openAIAccount, current)
			require.False(t, ok, "current=%q", current)
			require.Empty(t, tier)
		}
	})

	t.Run("force raises missing and cheaper tiers to priority", func(t *testing.T) {
		for _, current := range []string{"", "default", "auto", "scale", "flex", "fast", "priority", " Priority "} {
			tier, ok := openAIGroupForcedServiceTier(forceFastContext(true), openAIAccount, current)
			require.True(t, ok, "current=%q", current)
			require.Equal(t, OpenAIFastTierPriority, tier, "current=%q", current)
		}
	})

	// The regression this guards: a ForceOpenAIFast group used to rewrite every
	// request to priority, so an ultrafast group silently served and billed fast.
	t.Run("force never downgrades an explicit ultrafast request", func(t *testing.T) {
		for _, current := range []string{"ultrafast", "ULTRAFAST", " ultrafast "} {
			tier, ok := openAIGroupForcedServiceTier(forceFastContext(true), openAIAccount, current)
			require.False(t, ok, "current=%q", current)
			require.Empty(t, tier)
		}
	})

	t.Run("non-openai accounts are never forced", func(t *testing.T) {
		tier, ok := openAIGroupForcedServiceTier(forceFastContext(true), &Account{Platform: PlatformAnthropic}, "default")
		require.False(t, ok)
		require.Empty(t, tier)
	})

	// 显式「强制 UltraFast」：分组声明后，无论客户端传什么都要发 ultrafast。
	t.Run("explicit ultrafast target wins over the caller tier", func(t *testing.T) {
		for _, current := range []string{"", "default", "flex", "fast", "priority", "ultrafast"} {
			tier, ok := openAIGroupForcedServiceTier(forcedTierContext(OpenAIFastTierUltrafast, true), openAIAccount, current)
			require.True(t, ok, "current=%q", current)
			require.Equal(t, OpenAIFastTierUltrafast, tier, "current=%q", current)
		}
	})

	// 显式 priority 与旧开关等价，且不降级客户端已要求的 ultrafast。
	t.Run("explicit priority target matches the legacy switch", func(t *testing.T) {
		tier, ok := openAIGroupForcedServiceTier(forcedTierContext(OpenAIFastTierPriority, false), openAIAccount, "default")
		require.True(t, ok)
		require.Equal(t, OpenAIFastTierPriority, tier)

		tier, ok = openAIGroupForcedServiceTier(forcedTierContext(OpenAIFastTierPriority, false), openAIAccount, "ultrafast")
		require.False(t, ok)
		require.Empty(t, tier)
	})

	// 非法值回落为空：不强制，且不因旧开关而误触发。
	t.Run("unknown forced tier falls back to no-op", func(t *testing.T) {
		tier, ok := openAIGroupForcedServiceTier(forcedTierContext("turbo", false), openAIAccount, "default")
		require.False(t, ok)
		require.Empty(t, tier)
	})

	t.Run("missing account or context is a no-op", func(t *testing.T) {
		tier, ok := openAIGroupForcedServiceTier(context.Background(), openAIAccount, "default")
		require.False(t, ok)
		require.Empty(t, tier)
		tier, ok = openAIGroupForcedServiceTier(forceFastContext(true), nil, "default")
		require.False(t, ok)
		require.Empty(t, tier)
	})
}
