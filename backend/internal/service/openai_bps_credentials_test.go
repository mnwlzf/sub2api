package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openAIBPSTestJWT 构造只用于解析测试的 JWT（签名段不做任何校验）。
func openAIBPSTestJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(data) + ".test-signature"
}

func TestOpenAIBPSParseCredentialsJWT(t *testing.T) {
	exp := time.Unix(1900000000, 0).UTC()

	t.Run("嵌套 auth 提取账号 ID 与 exp", func(t *testing.T) {
		token := openAIBPSTestJWT(t, map[string]any{
			"exp": 1900000000,
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "acc-nested",
				"chatgpt_plan_type":  "plus",
			},
		})
		parsed := ParseOpenAIBPSCredentials(map[string]any{"access_token": token})
		require.Equal(t, token, parsed.AccessToken)
		require.Equal(t, "acc-nested", parsed.AccountID)
		require.NotNil(t, parsed.ExpiresAt)
		require.True(t, exp.Equal(*parsed.ExpiresAt))
	})

	t.Run("展平 claim 兼容", func(t *testing.T) {
		token := openAIBPSTestJWT(t, map[string]any{
			"https://api.openai.com/auth.chatgpt_account_id": "acc-flat",
		})
		require.Equal(t, "acc-flat", ParseOpenAIBPSCredentials(map[string]any{"access_token": token}).AccountID)
	})

	t.Run("顶层 claim 兼容", func(t *testing.T) {
		token := openAIBPSTestJWT(t, map[string]any{"chatgpt_account_id": "acc-top"})
		require.Equal(t, "acc-top", ParseOpenAIBPSCredentials(map[string]any{"access_token": token}).AccountID)
	})

	t.Run("无 exp 视为无到期信息", func(t *testing.T) {
		token := openAIBPSTestJWT(t, map[string]any{"sub": "user"})
		parsed := ParseOpenAIBPSCredentials(map[string]any{"access_token": token})
		require.Nil(t, parsed.ExpiresAt)
		require.Empty(t, parsed.AccountID)
	})

	t.Run("非法 token 返回空字段不报错", func(t *testing.T) {
		for _, token := range []string{
			"not-a-jwt",
			"a.b",
			"a.b.c.d",
			"aaa.!!!.ccc",
			"aaa." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".ccc",
		} {
			parsed := ParseOpenAIBPSCredentials(map[string]any{"access_token": token})
			require.Empty(t, parsed.AccountID)
			require.Nil(t, parsed.ExpiresAt)
		}
	})

	t.Run("Bearer 前缀被剥离", func(t *testing.T) {
		token := openAIBPSTestJWT(t, map[string]any{"chatgpt_account_id": "acc-bearer"})
		parsed := ParseOpenAIBPSCredentials(map[string]any{"access_token": "  Bearer " + token + "  "})
		require.Equal(t, token, parsed.AccessToken)
		require.Equal(t, "acc-bearer", parsed.AccountID)
	})

	t.Run("带 padding 的 payload 兼容", func(t *testing.T) {
		data := []byte(`{"chatgpt_account_id":"acc-padded"}`)
		padded := base64.URLEncoding.EncodeToString(data)
		require.Contains(t, padded, "=")
		token := "header." + padded + ".sig"
		require.Equal(t, "acc-padded", ParseOpenAIBPSCredentials(map[string]any{"access_token": token}).AccountID)
	})

	t.Run("exp 字符串兼容", func(t *testing.T) {
		token := openAIBPSTestJWT(t, map[string]any{"exp": "1900000000"})
		parsed := ParseOpenAIBPSCredentials(map[string]any{"access_token": token})
		require.NotNil(t, parsed.ExpiresAt)
		require.True(t, exp.Equal(*parsed.ExpiresAt))
	})

	t.Run("exp 越界忽略", func(t *testing.T) {
		for _, value := range []any{float64(253402300800), float64(-62135596801), "NaN", "Inf"} {
			token := openAIBPSTestJWT(t, map[string]any{"exp": value})
			require.Nil(t, ParseOpenAIBPSCredentials(map[string]any{"access_token": token}).ExpiresAt, "exp=%v", value)
		}
	})

	t.Run("nil 凭证返回零值", func(t *testing.T) {
		require.Equal(t, OpenAIBPSCredentials{}, ParseOpenAIBPSCredentials(nil))
	})
}

func TestOpenAIBPSAccountIDOverride(t *testing.T) {
	token := openAIBPSTestJWT(t, map[string]any{
		"exp":                         1900000000,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-jwt"},
	})

	t.Run("credentials 覆盖 JWT", func(t *testing.T) {
		credentials := map[string]any{"access_token": token, "chatgpt_account_id": "acc-override"}
		require.Equal(t, "acc-override", ParseOpenAIBPSCredentials(credentials).AccountID)
		account := &Account{Platform: PlatformOpenAIBPS, Credentials: credentials}
		require.Equal(t, "acc-override", account.OpenAIBPSAccountID())
	})

	t.Run("缺省回退 JWT", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAIBPS, Credentials: map[string]any{"access_token": token}}
		require.Equal(t, "acc-jwt", account.OpenAIBPSAccountID())
		require.Equal(t, token, account.OpenAIBPSAccessToken())
		require.NotNil(t, account.OpenAIBPSExpiresAt())
	})

	t.Run("空字符串覆盖不生效", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAIBPS, Credentials: map[string]any{
			"access_token":       token,
			"chatgpt_account_id": "   ",
		}}
		require.Equal(t, "acc-jwt", account.OpenAIBPSAccountID())
	})

	t.Run("nil 账号安全", func(t *testing.T) {
		var account *Account
		require.Empty(t, account.OpenAIBPSAccessToken())
		require.Empty(t, account.OpenAIBPSAccountID())
		require.Nil(t, account.OpenAIBPSExpiresAt())
	})
}

func TestOpenAIBPSRedactCredentials(t *testing.T) {
	exp := time.Unix(1900000000, 0).UTC()
	source := map[string]any{
		"access_token":       "secret-access-token",
		"chatgpt_account_id": "acc-workspace",
		"expires_at":         exp.Format(time.RFC3339),
		"refresh_token":      "secret-refresh-token",
		"id_token":           "secret-id-token",
	}

	redacted := RedactOpenAIBPSCredentials(source)
	require.Equal(t, OpenAIBPSRedactedAccessTokenPlaceholder, redacted["access_token"])
	require.Equal(t, "acc-workspace", redacted["chatgpt_account_id"])
	require.Equal(t, exp.Format(time.RFC3339), redacted["expires_at"])
	require.NotContains(t, redacted, "refresh_token")
	require.NotContains(t, redacted, "id_token")
	// 入参不得被修改。
	require.Equal(t, "secret-access-token", source["access_token"])
	require.Contains(t, source, "refresh_token")

	require.Nil(t, RedactOpenAIBPSCredentials(nil))
	empty := RedactOpenAIBPSCredentials(map[string]any{"access_token": "  ", "chatgpt_account_id": "acc"})
	require.NotContains(t, empty, "access_token")
	require.Equal(t, "acc", empty["chatgpt_account_id"])
}

func TestOpenAIBPSCredentialTokenFingerprint(t *testing.T) {
	first := OpenAIBPSCredentialTokenFingerprint("access-token-a")
	require.Len(t, first, 32)
	require.Regexp(t, `^[0-9a-f]{32}$`, first)
	require.Equal(t, first, OpenAIBPSCredentialTokenFingerprint("access-token-a"))
	require.Equal(t, first, OpenAIBPSCredentialTokenFingerprint("  Bearer access-token-a  "))
	require.NotEqual(t, first, OpenAIBPSCredentialTokenFingerprint("access-token-b"))
}
