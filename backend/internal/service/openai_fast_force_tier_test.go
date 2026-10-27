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

	t.Run("missing account or context is a no-op", func(t *testing.T) {
		tier, ok := openAIGroupForcedServiceTier(context.Background(), openAIAccount, "default")
		require.False(t, ok)
		require.Empty(t, tier)
		tier, ok = openAIGroupForcedServiceTier(forceFastContext(true), nil, "default")
		require.False(t, ok)
		require.Empty(t, tier)
	})
}
