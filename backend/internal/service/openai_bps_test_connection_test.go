package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openAIBPSTestRoundTripper 用函数实现 RoundTripper，模拟网络层失败。
type openAIBPSTestRoundTripper func(*http.Request) (*http.Response, error)

func (f openAIBPSTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// openAIBPSTestConnectionAccount 构造只带 BPS 必需凭证的账号。
func openAIBPSTestConnectionAccount(t *testing.T, accountID string) *Account {
	t.Helper()
	token := openAIBPSTestJWT(t, map[string]any{
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-jwt"},
	})
	credentials := map[string]any{
		"access_token": token,
		// 非 BPS 凭证键不得参与探测请求。
		"api_key":          "sk-should-not-be-used",
		"header_overrides": map[string]any{"x-custom": "should-not-be-sent"},
	}
	if accountID != "" {
		credentials["chatgpt_account_id"] = accountID
	}
	return &Account{ID: 9, Platform: PlatformOpenAIBPS, Type: AccountTypeOAuth, Credentials: credentials}
}

func TestOpenAIBPSTestConnection(t *testing.T) {
	t.Run("文本探测成功且只注入服务端鉴权头", func(t *testing.T) {
		var gotPath string
		var gotHeader http.Header
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotHeader = r.Header.Clone()
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("x-request-id", "rid-text")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"model":"gpt-6-astra","output":[]}`)
		}))
		defer server.Close()

		account := openAIBPSTestConnectionAccount(t, "acc-override")
		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", account, "", false, server.Client())

		require.Equal(t, "/basispoints/api/responses", gotPath)
		require.Equal(t, http.StatusOK, result.HTTPStatus)
		require.Equal(t, "gpt-6-astra", result.Model)
		require.False(t, result.Compact)
		require.Equal(t, "gpt-6-astra", result.UpstreamModel)
		require.Equal(t, "rid-text", result.RequestID)
		require.Empty(t, result.ErrorCode)
		require.GreaterOrEqual(t, result.DurationMS, 0)

		require.Equal(t, "Bearer "+account.OpenAIBPSAccessToken(), gotHeader.Get("Authorization"))
		require.Equal(t, "acc-override", gotHeader.Get("chatgpt-account-id"))
		require.Equal(t, OpenAIBPSAuthModeChatGPT, gotHeader.Get(OpenAIBPSAuthModeHeader))
		require.Equal(t, "application/json", gotHeader.Get("Content-Type"))
		// 客户端/账号上的其它认证材料不参与探测。
		require.Empty(t, gotHeader.Get("x-api-key"))
		require.Empty(t, gotHeader.Get("x-custom"))

		require.Equal(t, "gpt-6-astra", gotBody["model"])
		require.Equal(t, openAIBPSTestInput, gotBody["input"])
		require.Equal(t, false, gotBody["stream"])
	})

	t.Run("compact 探测走 /compact 路径", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("x-request-id", "rid-compact")
			_, _ = io.WriteString(w, `{"model":"gpt-5.6-sol"}`)
		}))
		defer server.Close()

		account := openAIBPSTestConnectionAccount(t, "acc-compact")
		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", account, "gpt-5.6-sol", true, server.Client())

		require.Equal(t, "/basispoints/api/responses/compact", gotPath)
		require.True(t, result.Compact)
		require.Equal(t, http.StatusOK, result.HTTPStatus)
		require.Equal(t, "gpt-5.6-sol", result.UpstreamModel)
	})

	t.Run("401 保留上游原始错误码", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-request-id", "rid-401")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_token","message":"token revoked"}}`)
		}))
		defer server.Close()

		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, server.Client())
		require.Equal(t, http.StatusUnauthorized, result.HTTPStatus)
		require.Equal(t, "invalid_token", result.ErrorCode)
		require.Equal(t, "rid-401", result.RequestID)
	})

	t.Run("401 无错误体兜底 token_revoked", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, server.Client())
		require.Equal(t, http.StatusUnauthorized, result.HTTPStatus)
		require.Equal(t, "token_revoked", result.ErrorCode)
	})

	t.Run("其它错误保留上游类型", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"type":"forbidden","message":"no access"}}`)
		}))
		defer server.Close()

		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, server.Client())
		require.Equal(t, http.StatusForbidden, result.HTTPStatus)
		require.Equal(t, "forbidden", result.ErrorCode)
	})

	t.Run("非 JSON 错误体按状态码兜底", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `<html>bad gateway</html>`)
		}))
		defer server.Close()

		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, server.Client())
		require.Equal(t, http.StatusBadGateway, result.HTTPStatus)
		require.Equal(t, "upstream_http_502", result.ErrorCode)
	})

	t.Run("2xx 非 JSON 响应判为无效响应", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `not-json`)
		}))
		defer server.Close()

		result := runOpenAIBPSTestConnection(context.Background(),
			server.URL+"/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, server.Client())
		require.Equal(t, http.StatusOK, result.HTTPStatus)
		require.Equal(t, "bps_invalid_response", result.ErrorCode)
	})

	t.Run("网络错误映射为不可达", func(t *testing.T) {
		client := &http.Client{Transport: openAIBPSTestRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: connection refused")
		})}
		result := runOpenAIBPSTestConnection(context.Background(),
			"https://bps.openai.com/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, client)
		require.Zero(t, result.HTTPStatus)
		require.Equal(t, "bps_upstream_unreachable", result.ErrorCode)
	})

	t.Run("超时映射为 upstream_timeout", func(t *testing.T) {
		client := &http.Client{Transport: openAIBPSTestRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})}
		result := runOpenAIBPSTestConnection(context.Background(),
			"https://bps.openai.com/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, client)
		require.Zero(t, result.HTTPStatus)
		require.Equal(t, "bps_upstream_timeout", result.ErrorCode)
	})

	t.Run("取消映射为 request_canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &http.Client{Transport: openAIBPSTestRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, context.Canceled
		})}
		result := runOpenAIBPSTestConnection(ctx,
			"https://bps.openai.com/basispoints/api/responses", openAIBPSTestConnectionAccount(t, "acc"), "", false, client)
		require.Equal(t, "bps_request_canceled", result.ErrorCode)
	})

	t.Run("本地校验失败不发起请求", func(t *testing.T) {
		cases := []struct {
			name    string
			account *Account
			code    string
		}{
			{"nil 账号", nil, "bps_account_invalid"},
			{"非 BPS 账号", &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"access_token": "x"}}, "bps_account_invalid"},
			{"缺少 token", &Account{Platform: PlatformOpenAIBPS, Credentials: map[string]any{}}, "bps_access_token_missing"},
			{"缺少账号 ID", &Account{Platform: PlatformOpenAIBPS, Credentials: map[string]any{"access_token": "opaque-token"}}, "bps_account_id_missing"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				result := runOpenAIBPSTestConnection(context.Background(),
					"https://bps.openai.com/basispoints/api/responses", tc.account, "", false, nil)
				require.Zero(t, result.HTTPStatus)
				require.Equal(t, tc.code, result.ErrorCode)
			})
		}
	})

	t.Run("导出入口在本地校验失败时不触网", func(t *testing.T) {
		result := RunOpenAIBPSTestConnection(context.Background(), nil, "", false, nil)
		require.Equal(t, "bps_account_invalid", result.ErrorCode)
		require.Equal(t, "gpt-6-astra", result.Model, "空模型回退到默认候选")
	})
}

// 确认探测请求体只包含约定字段，防止把客户端字段带进固定上游。
func TestOpenAIBPSTestConnectionRequestBodyWhitelist(t *testing.T) {
	var raw map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = io.WriteString(w, `{"model":"gpt-6-astra"}`)
	}))
	defer server.Close()

	result := runOpenAIBPSTestConnection(context.Background(),
		strings.TrimSuffix(server.URL, "/")+"/basispoints/api/responses",
		openAIBPSTestConnectionAccount(t, "acc"), "gpt-6-astra", false, server.Client())
	require.Equal(t, http.StatusOK, result.HTTPStatus)
	require.ElementsMatch(t, []string{"model", "input", "stream"}, keysOfOpenAIBPSTestMap(raw))
}
