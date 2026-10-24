package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func openAIRerankTestAccount(baseURL string, mapping map[string]any) *Account {
	return openAIEmbeddingsTestAccount(baseURL, mapping)
}

func TestNormalizeAccountTestMode_Rerank(t *testing.T) {
	t.Parallel()

	require.Equal(t, AccountTestModeRerank, normalizeAccountTestMode("rerank"))
	require.Equal(t, AccountTestModeRerank, normalizeAccountTestMode("  ReRaNk "))
	require.Equal(t, AccountTestModeDefault, normalizeAccountTestMode(""))
	require.Equal(t, AccountTestModeEmbeddings, normalizeAccountTestMode("embeddings"))
}

func TestResolveOpenAIRerankTestModel(t *testing.T) {
	t.Parallel()

	mapped := openAIRerankTestAccount("http://upstream", map[string]any{
		"zhipu-rerank": "rerank-3",
		"bge-reranker": "BAAI/bge-reranker-v2-m3",
	})
	// 显式选择走账号模型映射。
	require.Equal(t, "rerank-3", resolveOpenAIRerankTestModel(mapped, "zhipu-rerank"))
	// 未显式选择时按排序后的首个 rerank 键取映射模型（bge-reranker < zhipu-rerank）。
	require.Equal(t, "BAAI/bge-reranker-v2-m3", resolveOpenAIRerankTestModel(mapped, ""))

	// 映射里没有 rerank 类模型时回落到默认模型。
	chatOnly := openAIRerankTestAccount("http://upstream", map[string]any{"gpt-5.4": "gpt-5.4-upstream"})
	require.Equal(t, defaultOpenAIRerankTestModel, resolveOpenAIRerankTestModel(chatOnly, ""))

	// 无映射时同样使用默认模型。
	require.Equal(t, defaultOpenAIRerankTestModel, resolveOpenAIRerankTestModel(
		openAIRerankTestAccount("http://upstream", nil), ""))
}

func TestOpenAIBPSLooksLikeRerankModel(t *testing.T) {
	t.Parallel()

	for _, model := range []string{
		"bge-reranker-v2-m3",
		"BAAI/bge-reranker-large",
		"jina-reranker-v2-base-multilingual",
		"gte-rerank",
		"rerank-multilingual-v3.0",
	} {
		require.True(t, openAIBPSLooksLikeRerankModel(model), model)
	}
	for _, model := range []string{"gpt-5.4", "text-embedding-3-small", "bge-m3", ""} {
		require.False(t, openAIBPSLooksLikeRerankModel(model), model)
	}
}

func TestTestAccountConnection_RerankModeSuccess(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotQuery string
	var gotDocuments []gjson.Result
	var gotTopN float64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		gotQuery = gjson.GetBytes(body, "query").String()
		gotDocuments = gjson.GetBytes(body, "documents").Array()
		gotTopN = gjson.GetBytes(body, "top_n").Float()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.98},{"index":1,"relevance_score":0.02}],"usage":{"total_tokens":12}}`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeRerank)

	require.NoError(t, err)
	require.Equal(t, "/v1/rerank", gotPath)
	require.Equal(t, "Bearer sk-test", gotAuth)
	require.Equal(t, defaultOpenAIRerankTestModel, gotModel)
	require.Equal(t, openAIRerankTestQuery, gotQuery)
	require.Len(t, gotDocuments, 2)
	require.Equal(t, float64(2), gotTopN)

	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"test_start"`)
	require.Contains(t, sse, `"type":"status"`)
	require.Contains(t, sse, `"type":"content"`)
	require.Contains(t, sse, "Rerank OK")
	require.Contains(t, sse, "results=2")
	require.Contains(t, sse, "top_score=0.98")
	require.Contains(t, sse, `"type":"test_complete"`)
	require.Contains(t, sse, `"success":true`)
}

func TestTestAccountConnection_RerankModeUsesFirstMappingModel(t *testing.T) {
	var gotModel string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.9}]}`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, map[string]any{
		"zhipu-rerank": "rerank-3",
		"bge-reranker": "BAAI/bge-reranker-v2-m3",
	})
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeRerank)

	require.NoError(t, err)
	require.Equal(t, "BAAI/bge-reranker-v2-m3", gotModel)
	require.Contains(t, rec.Body.String(), "Rerank OK")
}

func TestTestAccountConnection_RerankModeExplicitModelIsMapped(t *testing.T) {
	var gotModel string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"score":0.7}]}`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, map[string]any{
		"bge-reranker-v2-m3": "BAAI/bge-reranker-v2-m3",
	})
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "bge-reranker-v2-m3", "", AccountTestModeRerank)

	require.NoError(t, err)
	require.Equal(t, "BAAI/bge-reranker-v2-m3", gotModel)
	// score 字段同样作为 top_score 展示。
	require.Contains(t, rec.Body.String(), "top_score=0.7")
}

func TestTestAccountConnection_RerankModeUpstream401(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeRerank)

	require.Error(t, err)
	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"error"`)
	require.Contains(t, sse, `"code":"401"`)
	require.Contains(t, sse, "returned 401")
	require.Contains(t, sse, "invalid_api_key")
	require.NotContains(t, sse, `"type":"test_complete"`)
}

func TestTestAccountConnection_RerankModeInvalidResponse(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeRerank)

	require.Error(t, err)
	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"error"`)
	require.Contains(t, sse, "no results array")
	require.NotContains(t, sse, `"type":"test_complete"`)
}

func TestTestAccountConnection_RerankModeRejectsOAuth(t *testing.T) {
	account := &Account{
		ID:          73,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeRerank)

	require.Error(t, err)
	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"error"`)
	require.Contains(t, sse, "requires an OpenAI API-key account")
}

func TestTestAccountConnection_RerankModeDispatchesViaOpenAIPath(t *testing.T) {
	var gotPath string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.5}]}`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.testOpenAIAccountConnection(c, account, "", "", AccountTestModeRerank)

	require.NoError(t, err)
	require.Equal(t, "/v1/rerank", gotPath)
	require.Contains(t, rec.Body.String(), "Rerank OK")
}

func TestTestAccountConnection_RerankModeInvalidResponseParsesAsJSON(t *testing.T) {
	// 上游返回 200 但完全不是 JSON：错误事件必须可被前端解析（不产生半个 JSON）。
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>gateway error</html>`))
	}))
	defer upstreamServer.Close()

	account := openAIRerankTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeRerank)

	require.Error(t, err)
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		require.True(t, json.Valid([]byte(payload)), "SSE payload must be valid JSON: %s", payload)
	}
}
