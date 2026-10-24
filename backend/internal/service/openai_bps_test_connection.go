package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// BPS 连接测试：保存账号后向固定上游发一次最小请求，验证凭证可用性。
//
// 测试本身不改写账号状态，只返回客观结果（HTTP 状态、上游模型、请求 ID、
// 原始错误码）；是否把 401 记为凭证失效由调用方（网关/管理端）决定。
const (
	// openAIBPSTestInput 是探测请求的最小输入，避免消耗真实 token 额度。
	openAIBPSTestInput = "Reply with OK."
	// openAIBPSTestMaxResponseBytes 限制读取的响应体大小，防止异常上游打爆内存。
	openAIBPSTestMaxResponseBytes = 1 << 20
	// openAIBPSTestDefaultTimeout 是未提供 http.Client 时的默认超时。
	openAIBPSTestDefaultTimeout = 30 * time.Second

	// 连接测试在本地校验失败时使用的稳定错误码。
	openAIBPSTestErrorAccountInvalid  = "bps_account_invalid"
	openAIBPSTestErrorTokenMissing    = "bps_access_token_missing"
	openAIBPSTestErrorAccountIDMiss   = "bps_account_id_missing"
	openAIBPSTestErrorRequestInvalid  = "bps_test_request_invalid"
	openAIBPSTestErrorUpstreamTimeout = "bps_upstream_timeout"
	openAIBPSTestErrorCanceled        = "bps_request_canceled"
	openAIBPSTestErrorUnreachable     = "bps_upstream_unreachable"
	openAIBPSTestErrorUnreadable      = "bps_upstream_response_unreadable"
	openAIBPSTestErrorInvalidResponse = "bps_invalid_response"
	// openAIBPSTestErrorRevoked 是 401 且上游未给出可识别错误码时的兜底。
	openAIBPSTestErrorRevoked = "token_revoked"
)

// OpenAIBPSTestResult 是一次 BPS 连接测试的结果。
//
// HTTPStatus 为 0 表示请求没有到达上游（网络/超时/本地校验失败）；此时
// ErrorCode 给出稳定原因。2xx 之外的 ErrorCode 优先保留上游原始错误码。
type OpenAIBPSTestResult struct {
	Model         string `json:"model"`
	Compact       bool   `json:"compact"`
	HTTPStatus    int    `json:"http_status"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	DurationMS    int    `json:"duration_ms"`
}

// RunOpenAIBPSTestConnection 用固定上游 OpenAIBPSUpstreamURL 测试账号连接。
//
// compact=true 时探测 /compact 路径。client 由调用方注入（可携带账号代理），
// nil 时使用带默认超时的客户端。函数不 panic、不返回 error：所有失败都体现在
// HTTPStatus/ErrorCode 中，调用方无需区分"测试失败"与"测试本身出错"。
func RunOpenAIBPSTestConnection(ctx context.Context, account *Account, model string, compact bool, client *http.Client) OpenAIBPSTestResult {
	return runOpenAIBPSTestConnection(ctx, OpenAIBPSUpstreamURL, account, model, compact, client)
}

// runOpenAIBPSTestConnection 是 RunOpenAIBPSTestConnection 的可注入端点实现。
// 生产路径固定使用 OpenAIBPSUpstreamURL；单测通过 httptest 端点验证请求形状。
func runOpenAIBPSTestConnection(
	ctx context.Context,
	endpoint string,
	account *Account,
	model string,
	compact bool,
	client *http.Client,
) OpenAIBPSTestResult {
	started := time.Now()
	result := OpenAIBPSTestResult{Model: strings.TrimSpace(model), Compact: compact}
	if result.Model == "" {
		if models := OpenAIBPSDefaultModels(); len(models) > 0 {
			result.Model = models[0]
		}
	}
	finish := func() OpenAIBPSTestResult {
		result.DurationMS = int(time.Since(started).Milliseconds())
		return result
	}

	if account == nil || !account.IsOpenAIBPS() {
		result.ErrorCode = openAIBPSTestErrorAccountInvalid
		return finish()
	}
	accessToken := account.OpenAIBPSAccessToken()
	if accessToken == "" {
		result.ErrorCode = openAIBPSTestErrorTokenMissing
		return finish()
	}
	accountID := account.OpenAIBPSAccountID()
	if accountID == "" {
		result.ErrorCode = openAIBPSTestErrorAccountIDMiss
		return finish()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(map[string]any{
		"model":  result.Model,
		"input":  openAIBPSTestInput,
		"stream": false,
	})
	if err != nil {
		result.ErrorCode = openAIBPSTestErrorRequestInvalid
		return finish()
	}
	url := strings.TrimSuffix(strings.TrimSpace(endpoint), "/")
	if compact {
		url += "/compact"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		result.ErrorCode = openAIBPSTestErrorRequestInvalid
		return finish()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("chatgpt-account-id", accountID)
	request.Header.Set(OpenAIBPSAuthModeHeader, OpenAIBPSAuthModeChatGPT)

	response, err := openAIBPSTestHTTPClient(client).Do(request)
	if err != nil {
		result.ErrorCode = openAIBPSTestTransportErrorCode(ctx, err)
		return finish()
	}
	defer func() { _ = response.Body.Close() }()
	result.HTTPStatus = response.StatusCode
	result.RequestID = openAIBPSResponseRequestID(response.Header)

	body, err := io.ReadAll(io.LimitReader(response.Body, openAIBPSTestMaxResponseBytes))
	if err != nil {
		result.ErrorCode = openAIBPSTestErrorUnreadable
		return finish()
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		var decoded struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			result.ErrorCode = openAIBPSTestErrorInvalidResponse
			return finish()
		}
		result.UpstreamModel = strings.TrimSpace(decoded.Model)
		return finish()
	}
	result.ErrorCode = openAIBPSTestUpstreamErrorCode(response.StatusCode, body)
	return finish()
}

// openAIBPSTestHTTPClient 返回实际使用的客户端；nil 时补一个带默认超时的实例。
func openAIBPSTestHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: openAIBPSTestDefaultTimeout}
}

// openAIBPSTestTransportErrorCode 把传输层错误映射为稳定错误码。
// 不吞错误：网络失败必须落到明确的 ErrorCode，而不是空字符串。
func openAIBPSTestTransportErrorCode(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return openAIBPSTestErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return openAIBPSTestErrorUpstreamTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return openAIBPSTestErrorUpstreamTimeout
	}
	return openAIBPSTestErrorUnreachable
}

// openAIBPSResponseRequestID 提取上游请求 ID，按常见头名依次回退。
func openAIBPSResponseRequestID(header http.Header) string {
	for _, key := range []string{"x-request-id", "openai-request-id", "request-id"} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

// openAIBPSTestUpstreamErrorCode 保留上游原始错误码；缺失时按状态码兜底。
// 401 且无错误码时返回 token_revoked，方便调用方直接记为 revoked。
func openAIBPSTestUpstreamErrorCode(status int, body []byte) string {
	var decoded struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil {
		if code := strings.TrimSpace(decoded.Error.Code); code != "" {
			return code
		}
		if codeType := strings.TrimSpace(decoded.Error.Type); codeType != "" {
			return codeType
		}
	}
	if status == http.StatusUnauthorized {
		return openAIBPSTestErrorRevoked
	}
	return fmt.Sprintf("upstream_http_%d", status)
}
