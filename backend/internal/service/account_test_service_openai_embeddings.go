package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// AccountTestModeEmbeddings drives the /v1/embeddings connection test. It is an
// explicit mode because embeddings accounts commonly expose only embedding
// models, so the generic Responses/Chat probe would fail for a healthy account.
const AccountTestModeEmbeddings = "embeddings"

// defaultOpenAIEmbeddingsTestModel is used when the admin neither picks a model
// nor has a model_mapping on the account.
const defaultOpenAIEmbeddingsTestModel = "text-embedding-3-small"

// openAIEmbeddingsTestInput is a minimal, deterministic embedding input.
const openAIEmbeddingsTestInput = "hello"

// resolveOpenAIEmbeddingsTestModel picks the model sent to /v1/embeddings:
//   - explicit admin selection wins (and is mapped like every other test path);
//   - otherwise the account's first model_mapping key (sorted for determinism,
//     since Go map iteration order is random);
//   - otherwise the OpenAI default embedding model.
func resolveOpenAIEmbeddingsTestModel(account *Account, modelID string) string {
	if model := strings.TrimSpace(modelID); model != "" {
		return account.GetMappedModel(model)
	}
	if mapping := account.GetModelMapping(); len(mapping) > 0 {
		keys := make([]string, 0, len(mapping))
		for key := range mapping {
			if strings.TrimSpace(key) != "" {
				keys = append(keys, key)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			return account.GetMappedModel(keys[0])
		}
	}
	return account.GetMappedModel(defaultOpenAIEmbeddingsTestModel)
}

// testOpenAIEmbeddingsConnection probes the account's OpenAI-compatible
// /v1/embeddings endpoint. Only API-key accounts can be probed: OAuth/Codex
// accounts have no OpenAI-format base_url + Bearer key pair, so they are
// rejected with an explicit message instead of a confusing upstream error.
func (s *AccountTestService) testOpenAIEmbeddingsConnection(c *gin.Context, account *Account, modelID string) error {
	ctx := c.Request.Context()

	credentialAccount := account
	if account.IsCredentialShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		credentialAccount = resolved
	}

	if credentialAccount.IsOAuth() || credentialAccount.Type != AccountTypeAPIKey {
		return s.sendErrorAndEnd(c, fmt.Sprintf(
			"Embeddings connection test requires an OpenAI API-key account; account type %q has no /v1/embeddings endpoint",
			credentialAccount.Type,
		))
	}

	authToken := strings.TrimSpace(credentialAccount.GetOpenAIProtocolAPIKey())
	if authToken == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}

	baseURL := credentialAccount.GetOpenAIFormatBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
	}
	apiURL := buildOpenAIEndpointURL(normalizedBaseURL, "/v1/embeddings")

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	testModelID := resolveOpenAIEmbeddingsTestModel(credentialAccount, modelID)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 /v1/embeddings 测试连接"})

	payloadBytes, err := json.Marshal(map[string]any{
		"model": testModelID,
		"input": openAIEmbeddingsTestInput,
	})
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Embeddings test payload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Embeddings request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if customUA := strings.TrimSpace(credentialAccount.GetOpenAIUserAgent()); customUA != "" {
		req.Header.Set("User-Agent", customUA)
	}

	// 账号级请求头覆写：测试请求与真实转发保持一致的最终头
	credentialAccount.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Embeddings API (/v1/embeddings) request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to read Embeddings response: %s", err.Error()))
	}

	if resp.StatusCode != http.StatusOK {
		// 保留上游状态码与错误码：管理员需要看到真实失败原因（401 鉴权、
		// 404 端点不存在、400 模型不支持等）。
		message := fmt.Sprintf(
			"Embeddings API (/v1/embeddings) returned %d: %s",
			resp.StatusCode,
			truncateString(strings.TrimSpace(string(body)), 2048),
		)
		logAccountTestError(c, message)
		s.sendEvent(c, TestEvent{Type: "error", Error: message, Code: strconv.Itoa(resp.StatusCode)})
		return fmt.Errorf("%s", message)
	}

	embedding := gjson.GetBytes(body, "data.0.embedding")
	if !embedding.Exists() || !embedding.IsArray() || len(embedding.Array()) == 0 {
		return s.sendErrorAndEnd(c, "Embeddings API (/v1/embeddings) returned no vector in data[0].embedding")
	}

	dimensions := len(embedding.Array())
	usageText := ""
	if total := gjson.GetBytes(body, "usage.total_tokens"); total.Exists() && total.Int() > 0 {
		usageText = fmt.Sprintf(", tokens=%d", total.Int())
	}
	s.sendEvent(c, TestEvent{
		Type: "content",
		Text: fmt.Sprintf("Embedding OK: model=%s, dimensions=%d%s", testModelID, dimensions, usageText),
	})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
