package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// openAIEmbeddingsTestRepo is an AccountRepository stub that only serves
// GetByID; the embeddings account test never touches other repository methods.
type openAIEmbeddingsTestRepo struct {
	AccountRepository
	account *Account
}

func (r *openAIEmbeddingsTestRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

// openAIEmbeddingsTestUpstream forwards to a real httptest server so the test
// exercises URL building, auth headers and response parsing end to end.
type openAIEmbeddingsTestUpstream struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
}

func (u *openAIEmbeddingsTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.DoWithTLS(req, "", 0, 0, nil)
}

func (u *openAIEmbeddingsTestUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	u.requests = append(u.requests, req)
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	return http.DefaultClient.Do(req)
}

func newOpenAIEmbeddingsTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	return c, rec
}

func newOpenAIEmbeddingsTestService(account *Account, upstream HTTPUpstream) *AccountTestService {
	return &AccountTestService{
		accountRepo:  &openAIEmbeddingsTestRepo{account: account},
		httpUpstream: upstream,
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true},
			},
		},
	}
}

func openAIEmbeddingsTestAccount(baseURL string, mapping map[string]any) *Account {
	credentials := map[string]any{
		"api_key":  "sk-test",
		"base_url": baseURL,
	}
	if mapping != nil {
		credentials["model_mapping"] = mapping
	}
	return &Account{
		ID:          71,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: credentials,
	}
}

func TestNormalizeAccountTestMode_Embeddings(t *testing.T) {
	t.Parallel()

	require.Equal(t, AccountTestModeEmbeddings, normalizeAccountTestMode("embeddings"))
	require.Equal(t, AccountTestModeEmbeddings, normalizeAccountTestMode("  EmBeDdInGs "))
	require.Equal(t, AccountTestModeDefault, normalizeAccountTestMode(""))
	require.Equal(t, AccountTestModeCompact, normalizeAccountTestMode("compact"))
}

func TestTestAccountConnection_EmbeddingsModeSuccess(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotInput string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		gotInput = gjson.GetBytes(body, "input").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`))
	}))
	defer upstreamServer.Close()

	account := openAIEmbeddingsTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeEmbeddings)

	require.NoError(t, err)
	require.Equal(t, "/v1/embeddings", gotPath)
	require.Equal(t, "Bearer sk-test", gotAuth)
	require.Equal(t, defaultOpenAIEmbeddingsTestModel, gotModel)
	require.Equal(t, "hello", gotInput)

	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"test_start"`)
	require.Contains(t, sse, `"type":"status"`)
	require.Contains(t, sse, `"type":"content"`)
	require.Contains(t, sse, "dimensions=3")
	require.Contains(t, sse, "tokens=7")
	require.Contains(t, sse, `"type":"test_complete"`)
	require.Contains(t, sse, `"success":true`)
}

func TestTestAccountConnection_EmbeddingsModeUsesFirstMappingModel(t *testing.T) {
	var gotModel string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2]}],"usage":{"total_tokens":2}}`))
	}))
	defer upstreamServer.Close()

	// 两个键：排序后 "bge-m3" 在前，因此默认测试模型应是它映射后的上游模型。
	account := openAIEmbeddingsTestAccount(upstreamServer.URL, map[string]any{
		"zhipu-embedding": "embedding-3",
		"bge-m3":          "BAAI/bge-m3",
	})
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeEmbeddings)

	require.NoError(t, err)
	require.Equal(t, "BAAI/bge-m3", gotModel)
	require.Contains(t, rec.Body.String(), "dimensions=2")
}

func TestTestAccountConnection_EmbeddingsModeExplicitModelIsMapped(t *testing.T) {
	var gotModel string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.5]}],"usage":{"total_tokens":1}}`))
	}))
	defer upstreamServer.Close()

	account := openAIEmbeddingsTestAccount(upstreamServer.URL, map[string]any{
		"text-embedding-3-small": "jina-embeddings-v5-text-small",
	})
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "text-embedding-3-small", "", AccountTestModeEmbeddings)

	require.NoError(t, err)
	require.Equal(t, "jina-embeddings-v5-text-small", gotModel)
	require.Contains(t, rec.Body.String(), "dimensions=1")
}

func TestTestAccountConnection_EmbeddingsModeUpstream401(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}`))
	}))
	defer upstreamServer.Close()

	account := openAIEmbeddingsTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeEmbeddings)

	require.Error(t, err)
	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"error"`)
	require.Contains(t, sse, `"code":"401"`)
	require.Contains(t, sse, "returned 401")
	require.Contains(t, sse, "invalid_api_key")
	require.NotContains(t, sse, `"type":"test_complete"`)
}

func TestTestAccountConnection_EmbeddingsModeInvalidResponse(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"index":0,"embedding":[]}]}`))
	}))
	defer upstreamServer.Close()

	account := openAIEmbeddingsTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeEmbeddings)

	require.Error(t, err)
	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"error"`)
	require.Contains(t, sse, "no vector in data[0].embedding")
	require.NotContains(t, sse, `"type":"test_complete"`)
}

func TestTestAccountConnection_EmbeddingsModeRejectsOAuth(t *testing.T) {
	account := &Account{
		ID:          72,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token"},
	}
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeEmbeddings)

	require.Error(t, err)
	sse := rec.Body.String()
	require.Contains(t, sse, `"type":"error"`)
	require.Contains(t, sse, "requires an OpenAI API-key account")
}

func TestTestAccountConnection_EmbeddingsModeInvalidResponseParsesAsJSON(t *testing.T) {
	// 上游返回 200 但完全不是 JSON：错误事件必须可被前端解析（不产生半个 JSON）。
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>gateway error</html>`))
	}))
	defer upstreamServer.Close()

	account := openAIEmbeddingsTestAccount(upstreamServer.URL, nil)
	svc := newOpenAIEmbeddingsTestService(account, &openAIEmbeddingsTestUpstream{})
	c, rec := newOpenAIEmbeddingsTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeEmbeddings)

	require.Error(t, err)
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		require.True(t, json.Valid([]byte(payload)), "SSE payload must be valid JSON: %s", payload)
	}
}
