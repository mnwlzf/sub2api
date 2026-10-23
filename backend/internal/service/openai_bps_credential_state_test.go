package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openAIBPSTestAccount 构造带 JWT 到期时间的 BPS 账号。
func openAIBPSTestAccount(t *testing.T, accountID string, expiresAt *time.Time, extra map[string]any) *Account {
	t.Helper()
	payload := map[string]any{}
	if expiresAt != nil {
		payload["exp"] = expiresAt.Unix()
	}
	if accountID != "" {
		payload["https://api.openai.com/auth"] = map[string]any{"chatgpt_account_id": accountID}
	}
	credentials := map[string]any{"access_token": openAIBPSTestJWT(t, payload)}
	if accountID != "" {
		credentials["chatgpt_account_id"] = accountID
	}
	return &Account{
		ID:          7,
		Platform:    PlatformOpenAIBPS,
		Type:        AccountTypeOAuth,
		Credentials: credentials,
		Extra:       extra,
	}
}

// openAIBPSTestSavedState 构造 extra 中的诊断状态（JSON 往返后的 map 形状）。
func openAIBPSTestSavedState(t *testing.T, account *Account, status OpenAIBPSCredentialStatus, errorCode string, seen time.Time) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"status":            status,
		"last_seen_at":      seen.UTC().Format(time.RFC3339Nano),
		"error_code":        errorCode,
		"token_fingerprint": OpenAIBPSCredentialTokenFingerprint(account.OpenAIBPSAccessToken()),
		"account_id":        account.OpenAIBPSAccountID(),
	})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	return decoded
}

func TestOpenAIBPSResolveCredentialState(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Hour)
	past := now.Add(-2 * time.Hour)

	t.Run("正常凭证", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-normal", &future, nil)
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusNormal, state.Status)
		require.Empty(t, state.ErrorCode)
		require.Nil(t, state.LastSeenAt)
		require.NotNil(t, state.ExpiresAt)
		require.True(t, future.Equal(*state.ExpiresAt))
	})

	t.Run("无 exp 视为正常", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-no-exp", nil, nil)
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusNormal, state.Status)
		require.Nil(t, state.ExpiresAt)
	})

	t.Run("本地过期", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-expired", &past, nil)
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusExpired, state.Status)
		require.Equal(t, openAIBPSExpiredErrorCode, state.ErrorCode)
	})

	t.Run("上游撤销", func(t *testing.T) {
		seen := now.Add(-time.Minute)
		account := openAIBPSTestAccount(t, "acc-revoked", &future, nil)
		account.Extra = map[string]any{
			OpenAIBPSCredentialStateExtraKey: openAIBPSTestSavedState(t, account, OpenAIBPSCredentialStatusRevoked, "token_revoked", seen),
		}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusRevoked, state.Status)
		require.Equal(t, "token_revoked", state.ErrorCode)
		require.NotNil(t, state.LastSeenAt)
		require.True(t, seen.Equal(*state.LastSeenAt))
	})

	t.Run("认证失败", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-auth-failed", &future, nil)
		account.Extra = map[string]any{
			OpenAIBPSCredentialStateExtraKey: openAIBPSTestSavedState(t, account, OpenAIBPSCredentialStatusAuthFailed, "invalid_token", now),
		}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusAuthFailed, state.Status)
		require.Equal(t, "invalid_token", state.ErrorCode)
	})

	t.Run("撤销优先于本地过期", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-revoked-expired", &past, nil)
		account.Extra = map[string]any{
			OpenAIBPSCredentialStateExtraKey: openAIBPSTestSavedState(t, account, OpenAIBPSCredentialStatusRevoked, "token_revoked", now),
		}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusRevoked, state.Status)
		require.Equal(t, "token_revoked", state.ErrorCode)
	})

	t.Run("认证失败后本地过期以过期展示", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-auth-expired", &past, nil)
		account.Extra = map[string]any{
			OpenAIBPSCredentialStateExtraKey: openAIBPSTestSavedState(t, account, OpenAIBPSCredentialStatusAuthFailed, "invalid_token", now),
		}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusExpired, state.Status)
		require.Equal(t, "invalid_token", state.ErrorCode, "保留原始错误码便于排障")
	})

	t.Run("token 已替换时陈旧诊断不生效", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-rotated", &future, nil)
		account.Extra = map[string]any{
			OpenAIBPSCredentialStateExtraKey: map[string]any{
				"status":            OpenAIBPSCredentialStatusRevoked,
				"error_code":        "token_revoked",
				"last_seen_at":      now.Format(time.RFC3339Nano),
				"token_fingerprint": OpenAIBPSCredentialTokenFingerprint("old-token"),
				"account_id":        "acc-rotated",
			},
		}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusNormal, state.Status)
		require.Empty(t, state.ErrorCode)
	})

	t.Run("账号 ID 已变更时陈旧诊断不生效", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-new", &future, nil)
		account.Extra = map[string]any{
			OpenAIBPSCredentialStateExtraKey: map[string]any{
				"status":            OpenAIBPSCredentialStatusRevoked,
				"error_code":        "token_revoked",
				"token_fingerprint": OpenAIBPSCredentialTokenFingerprint(account.OpenAIBPSAccessToken()),
				"account_id":        "acc-old",
			},
		}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusNormal, state.Status)
	})

	t.Run("畸形诊断按无诊断处理", func(t *testing.T) {
		account := openAIBPSTestAccount(t, "acc-bad-extra", &future, nil)
		account.Extra = map[string]any{OpenAIBPSCredentialStateExtraKey: "not-a-state"}
		state := ResolveOpenAIBPSCredentialState(account, now)
		require.Equal(t, OpenAIBPSCredentialStatusNormal, state.Status)
	})

	t.Run("非 BPS 账号返回零值", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"access_token": "x"}}
		require.Equal(t, OpenAIBPSCredentialState{}, ResolveOpenAIBPSCredentialState(account, now))
		require.Equal(t, OpenAIBPSCredentialState{}, ResolveOpenAIBPSCredentialState(nil, now))
	})
}

func TestOpenAIBPSCredentialStateExtra(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	t.Run("撤销状态映射", func(t *testing.T) {
		extra := NewOpenAIBPSCredentialStateExtra("token_revoked", "fingerprint", " acc ", now)
		require.Equal(t, OpenAIBPSCredentialStatusRevoked, extra.Status)
		require.Equal(t, "token_revoked", extra.ErrorCode)
		require.Equal(t, "acc", extra.AccountID)
		require.NotNil(t, extra.LastSeenAt)
		require.True(t, now.Equal(*extra.LastSeenAt))
	})

	t.Run("其它错误映射为认证失败", func(t *testing.T) {
		extra := NewOpenAIBPSCredentialStateExtra("invalid_token", "fp", "acc", now)
		require.Equal(t, OpenAIBPSCredentialStatusAuthFailed, extra.Status)
		require.Equal(t, "invalid_token", extra.ErrorCode)
	})

	t.Run("非法错误码回退默认值", func(t *testing.T) {
		for _, code := range []string{"", "  ", "INVALID TOKEN!", "错误码"} {
			extra := NewOpenAIBPSCredentialStateExtra(code, "fp", "acc", now)
			require.Equal(t, openAIBPSDefaultAuthErrorCode, extra.ErrorCode, "code=%q", code)
			require.Equal(t, OpenAIBPSCredentialStatusAuthFailed, extra.Status)
		}
	})

	t.Run("持久化 JSON 字段固定", func(t *testing.T) {
		encoded, err := json.Marshal(NewOpenAIBPSCredentialStateExtra("token_revoked", "fp", "acc", now))
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.ElementsMatch(t,
			[]string{"status", "last_seen_at", "error_code", "token_fingerprint", "account_id"},
			keysOfOpenAIBPSTestMap(decoded))
		require.Equal(t, "2026-10-23T12:00:00Z", decoded["last_seen_at"])
	})
}

func keysOfOpenAIBPSTestMap(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
