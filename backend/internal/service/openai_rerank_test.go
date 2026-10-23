package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIRerankURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		want string
	}{
		{"bare domain", "https://api.openai.com", "https://api.openai.com/v1/rerank"},
		{"bare /v1", "https://api.openai.com/v1", "https://api.openai.com/v1/rerank"},
		{"already rerank", "https://api.jina.ai/v1/rerank", "https://api.jina.ai/v1/rerank"},
		{"third-party versioned path", "https://api.siliconflow.cn/v1", "https://api.siliconflow.cn/v1/rerank"},
		{"third-party non-versioned path", "https://open.bigmodel.cn/api/paas/v4", "https://open.bigmodel.cn/api/paas/v4/rerank"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, buildOpenAIRerankURL(tt.base))
		})
	}
}

func TestForwardRerank_APIKeyPassthroughRecordsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reqBody := []byte(`{
		"model":"bge-reranker",
		"query":"what is go?",
		"documents":["go is a language","rust is a language"],
		"top_n":2
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `{
		"id":"rerank-1",
		"results":[{"index":0,"relevance_score":0.98},{"index":1,"relevance_score":0.12}],
		"meta":{"billed_units":{"input_tokens":17}},
		"usage":{"prompt_tokens":17,"total_tokens":17}
	}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"rerank-rid"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:       52,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.siliconflow.cn/v1",
			"model_mapping": map[string]any{
				"bge-reranker": "BAAI/bge-reranker-v2-m3",
			},
		},
	}

	result, err := svc.ForwardRerank(context.Background(), c, account, reqBody, "")

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, result)
	require.Equal(t, "rerank-rid", result.RequestID)
	require.Equal(t, "bge-reranker", result.Model)
	require.Equal(t, "BAAI/bge-reranker-v2-m3", result.BillingModel)
	require.Equal(t, "BAAI/bge-reranker-v2-m3", result.UpstreamModel)
	require.Equal(t, 17, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)
	require.Equal(t, "https://api.siliconflow.cn/v1/rerank", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "BAAI/bge-reranker-v2-m3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "what is go?", gjson.GetBytes(upstream.lastBody, "query").String())
	require.Equal(t, int64(2), gjson.GetBytes(upstream.lastBody, "documents.#").Int())
	// 上游响应体原样透传。
	require.JSONEq(t, upstreamBody, rec.Body.String())
}

func TestForwardRerank_NoUsageLeavesUsageEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reqBody := []byte(`{"model":"rerank-v1","query":"q","documents":["a"]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader(reqBody))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"results":[{"index":0,"relevance_score":0.5}]}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          53,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.jina.ai"},
	}

	result, err := svc.ForwardRerank(context.Background(), c, account, reqBody, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Equal(t, "https://api.jina.ai/v1/rerank", upstream.lastReq.URL.String())
}

func TestForwardRerank_MissingModelReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader([]byte(`{"query":"q","documents":["a"]}`)))

	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          54,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.ForwardRerank(context.Background(), c, account, []byte(`{"query":"q"}`), "")

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "model is required", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Empty(t, upstream.requests)
}

func TestForwardRerank_NonFailoverUpstreamErrorPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reqBody := []byte(`{"model":"rerank-v1","query":"q","documents":["a"]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader(reqBody))

	upstreamBody := `{"error":{"type":"invalid_request_error","code":"invalid_model","message":"model not supported"}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          55,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.jina.ai"},
	}

	result, err := svc.ForwardRerank(context.Background(), c, account, reqBody, "")

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr, "400 invalid_model must not fail over")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, upstreamBody, rec.Body.String())
}

func TestForwardRerank_AccessStateUsesTypedFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reqBody := []byte(`{"model":"rerank-v1","query":"q","documents":["a"]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", bytes.NewReader(reqBody))

	upstreamBody := []byte(`{"error":{"code":"deactivated_workspace","message":"Workspace is deactivated"}}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_rerank_access_state"},
		},
		Body: io.NopCloser(bytes.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          56,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	result, err := svc.ForwardRerank(context.Background(), c, account, reqBody, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.Equal(t, GatewayFailureStageAccountAuth, failoverErr.Stage)
	require.Equal(t, GatewayFailureScopeAccount, failoverErr.Scope)
	require.Equal(t, OpenAIUpstreamAccessStateReason, failoverErr.Reason)
	require.Equal(t, NextAccountRetry, failoverErr.NextAccountAction)
	require.Equal(t, "req_rerank_access_state", failoverErr.ResponseHeaders.Get("x-request-id"))
	require.False(t, c.Writer.Written())
}

func TestExtractOpenAIRerankUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantInput  int
		wantOutput int
	}{
		{
			name:      "openai usage",
			body:      `{"usage":{"prompt_tokens":13,"total_tokens":13}}`,
			wantInput: 13,
		},
		{
			name:      "openai input_tokens alias",
			body:      `{"usage":{"input_tokens":9}}`,
			wantInput: 9,
		},
		{
			name:      "cohere billed units",
			body:      `{"meta":{"billed_units":{"input_tokens":21}}}`,
			wantInput: 21,
		},
		{
			name:      "siliconflow meta tokens",
			body:      `{"meta":{"tokens":{"input_tokens":8,"output_tokens":0}}}`,
			wantInput: 8,
		},
		{
			name: "absent",
			body: `{"results":[]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			usage := extractOpenAIRerankUsage([]byte(tt.body))
			require.Equal(t, tt.wantInput, usage.InputTokens)
			require.Equal(t, tt.wantOutput, usage.OutputTokens)
		})
	}
}

func TestAccountSupportsOpenAIEndpointCapabilityRerank(t *testing.T) {
	t.Parallel()

	apiKey := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.jina.ai",
		},
	}
	require.True(t, apiKey.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityRerank))

	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, oauth.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityRerank))

	upstream := &Account{Platform: PlatformOpenAI, Type: AccountTypeUpstream}
	require.False(t, upstream.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityRerank))

	chatOnly := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"openai_capabilities": []any{"chat_completions"}},
	}
	require.False(t, chatOnly.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityRerank))

	rerankOnly := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"openai_capabilities": []any{"rerank"}},
	}
	require.True(t, rerankOnly.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityRerank))
	require.False(t, rerankOnly.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
}

func TestNormalizeBulkOpenAIEndpointCapabilities_Rerank(t *testing.T) {
	t.Parallel()

	t.Run("all three selected clears the restriction", func(t *testing.T) {
		t.Parallel()
		got, includeChat, err := normalizeBulkOpenAIEndpointCapabilities([]any{"chat_completions", "embeddings", "rerank"})
		require.NoError(t, err)
		require.Nil(t, got)
		require.True(t, includeChat)
	})

	t.Run("chat plus embeddings keeps the legacy unrestricted semantics", func(t *testing.T) {
		t.Parallel()
		got, includeChat, err := normalizeBulkOpenAIEndpointCapabilities([]any{"chat_completions", "embeddings"})
		require.NoError(t, err)
		require.Nil(t, got)
		require.True(t, includeChat)
	})

	t.Run("chat plus rerank is an explicit restriction", func(t *testing.T) {
		t.Parallel()
		got, includeChat, err := normalizeBulkOpenAIEndpointCapabilities([]any{"chat_completions", "rerank"})
		require.NoError(t, err)
		require.Equal(t, []string{"chat_completions", "rerank"}, got)
		require.True(t, includeChat)
	})

	t.Run("rerank only", func(t *testing.T) {
		t.Parallel()
		got, includeChat, err := normalizeBulkOpenAIEndpointCapabilities([]string{"rerank"})
		require.NoError(t, err)
		require.Equal(t, []string{"rerank"}, got)
		require.False(t, includeChat)
	})

	t.Run("unknown capability rejected", func(t *testing.T) {
		t.Parallel()
		_, _, err := normalizeBulkOpenAIEndpointCapabilities([]any{"rerank", "not_a_capability"})
		require.Error(t, err)
	})
}
