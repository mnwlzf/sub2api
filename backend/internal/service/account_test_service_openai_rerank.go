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

// AccountTestModeRerank drives the /v1/rerank connection test. Rerank-only
// accounts commonly expose just rerank models, so the generic Responses/Chat
// probe would fail for a healthy account.
const AccountTestModeRerank = "rerank"

// defaultOpenAIRerankTestModel is used when the admin neither picks a model nor
// has a model_mapping entry that looks like a rerank model.
const defaultOpenAIRerankTestModel = "bge-reranker-v2-m3"

// openAIRerankTestQuery is a minimal deterministic rerank query.
const openAIRerankTestQuery = "What is the capital of France?"

// openAIRerankTestDocuments is a minimal deterministic rerank corpus.
var openAIRerankTestDocuments = []string{
	"Paris is the capital of France.",
	"Berlin is the capital of Germany.",
}

// resolveOpenAIRerankTestModel picks the model sent to /v1/rerank:
//   - explicit admin selection wins (and is mapped like every other test path);
//   - otherwise the account's first model_mapping key that looks like a rerank
//     model (sorted for determinism, since Go map iteration order is random);
//   - otherwise the OpenAI-compatible default rerank model.
func resolveOpenAIRerankTestModel(account *Account, modelID string) string {
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
		sort.Strings(keys)
		for _, key := range keys {
			if openAIBPSLooksLikeRerankModel(key) {
				return account.GetMappedModel(key)
			}
		}
	}
	return account.GetMappedModel(defaultOpenAIRerankTestModel)
}

// openAIBPSLooksLikeRerankModel 判断模型名是否像 rerank 模型。
// 账号 model_mapping 常混有聊天/embedding 模型，测试默认模型优先挑 rerank 类：
// 覆盖 bge-reranker、jina-reranker、gte-rerank、rerank-*-v3.0 等常见命名。
func openAIBPSLooksLikeRerankModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, needle := range []string{"rerank", "ranker"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

// testOpenAIRerankConnection probes the account's OpenAI-compatible
// /v1/rerank endpoint. Only API-key accounts can be probed: OAuth/Codex
// accounts have no OpenAI-format base_url + Bearer key pair, so they are
// rejected with an explicit message instead of a confusing upstream error.
func (s *AccountTestService) testOpenAIRerankConnection(c *gin.Context, account *Account, modelID string) error {
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
			"Rerank connection test requires an OpenAI API-key account; account type %q has no /v1/rerank endpoint",
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
	apiURL := buildOpenAIEndpointURL(normalizedBaseURL, "/v1/rerank")

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	testModelID := resolveOpenAIRerankTestModel(credentialAccount, modelID)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 /v1/rerank 测试连接"})

	payloadBytes, err := json.Marshal(map[string]any{
		"model":     testModelID,
		"query":     openAIRerankTestQuery,
		"documents": openAIRerankTestDocuments,
		"top_n":     len(openAIRerankTestDocuments),
	})
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Rerank test payload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Rerank request")
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
		return s.sendErrorAndEnd(c, fmt.Sprintf("Rerank API (/v1/rerank) request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to read Rerank response: %s", err.Error()))
	}

	if resp.StatusCode != http.StatusOK {
		// 保留上游状态码与错误码：管理员需要看到真实失败原因（401 鉴权、
		// 404 端点不存在、400 模型不支持等）。
		message := fmt.Sprintf(
			"Rerank API (/v1/rerank) returned %d: %s",
			resp.StatusCode,
			truncateString(strings.TrimSpace(string(body)), 2048),
		)
		logAccountTestError(c, message)
		s.sendEvent(c, TestEvent{Type: "error", Error: message, Code: strconv.Itoa(resp.StatusCode)})
		return fmt.Errorf("%s", message)
	}

	results := gjson.GetBytes(body, "results")
	if !results.Exists() || !results.IsArray() || len(results.Array()) == 0 {
		return s.sendErrorAndEnd(c, "Rerank API (/v1/rerank) returned no results array")
	}
	// 兼容 Cohere/Jina 风格 relevance_score 与部分网关的 score 字段。
	topScore := gjson.GetBytes(body, "results.0.relevance_score")
	if !topScore.Exists() {
		topScore = gjson.GetBytes(body, "results.0.score")
	}
	scoreText := ""
	if topScore.Exists() {
		scoreText = fmt.Sprintf(", top_score=%s", topScore.Raw)
	}
	s.sendEvent(c, TestEvent{
		Type: "content",
		Text: fmt.Sprintf("Rerank OK: model=%s, results=%d%s", testModelID, len(results.Array()), scoreText),
	})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
