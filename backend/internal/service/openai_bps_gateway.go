package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// BPS 上游响应体/流的上限。BPS 只承载文本 Responses，超过该尺寸的响应按
// 上游协议错误处理，避免恶意/异常上游把进程内存打满。
const (
	openAIBPSMaxJSONResponseBytes = 32 << 20
	openAIBPSMaxSSELineBytes      = 16 << 20
	openAIBPSMaxSSEEventBytes     = 16 << 20
)

// BPS 上游鉴权/身份头。账号 ID 同时用两个头名（chatgpt-account-id 是文档头，
// x-openai-account-id 是 Excel 插件实际发送的兼容头），两个都由服务端从账号
// 凭据注入，绝不从客户端请求复制。
const (
	openAIBPSAccountIDHeader       = "chatgpt-account-id"
	openAIBPSCompatAccountIDHeader = "x-openai-account-id"
)

// 断流/转发错误码（响应体为 OpenAI 兼容错误信封时使用）。
const openAIBPSStreamInterruptedCode = "bps_stream_interrupted"

// 阶段 2 凭证解析层会在 *Account 上提供下列方法（本阶段尚未合并）。用可选
// 接口探测而不是直接调用，保证两个阶段的新文件合并后自动切换到 JWT 派生
// 实现，同时在阶段 3 单独构建时仍可编译。
type (
	openAIBPSAccountTokenProvider interface {
		OpenAIBPSAccessToken() string
	}
	openAIBPSAccountIDProvider interface {
		OpenAIBPSAccountID() string
	}
	openAIBPSAccountExpiryProvider interface {
		OpenAIBPSExpiresAt() *time.Time
	}
)

// openAIBPSAccessToken 读取账号的 BPS Access Token。
func openAIBPSAccessToken(account *Account) string {
	if account == nil {
		return ""
	}
	if provider, ok := any(account).(openAIBPSAccountTokenProvider); ok {
		return strings.TrimSpace(provider.OpenAIBPSAccessToken())
	}
	return strings.TrimSpace(stringValue(account.Credentials["access_token"]))
}

// openAIBPSWorkspace 读取账号绑定的 chatgpt workspace（账号 ID）。
// 阶段 2 合并后优先使用 JWT 派生值（credentials 显式覆盖仍优先）。
func openAIBPSWorkspace(account *Account) string {
	if account == nil {
		return ""
	}
	if provider, ok := any(account).(openAIBPSAccountIDProvider); ok {
		return strings.TrimSpace(provider.OpenAIBPSAccountID())
	}
	return strings.TrimSpace(stringValue(account.Credentials["chatgpt_account_id"]))
}

// openAIBPSCredentialExpired 报告账号凭据（JWT exp）是否已过期。
// 阶段 2 合并前没有该方法，返回 false（由上游 401 暴露）。
func openAIBPSCredentialExpired(account *Account) bool {
	if account == nil {
		return false
	}
	provider, ok := any(account).(openAIBPSAccountExpiryProvider)
	if !ok {
		return false
	}
	expiresAt := provider.OpenAIBPSExpiresAt()
	return expiresAt != nil && !time.Now().Before(*expiresAt)
}

// BuildOpenAIBPSUpstreamRequest 构造发往 BPS 固定上游的 Responses 请求。
//
// 契约（不可放宽）：
//   - 端点固定为 OpenAIBPSUpstreamURL，不接受账号级 base_url 或任何客户端参数；
//   - 鉴权头全部由服务端从账号凭据注入：Authorization、chatgpt-account-id、
//     x-openai-account-id、x-basispoints-auth-mode；
//   - 函数只接收请求体，不接收客户端 header，因此客户端认证头（Authorization/
//     x-api-key/cookie 等）结构上不可能进入上游；
//   - stream 决定 Accept：SSE 或 JSON（同一端点，BPS 没有独立的非流式端点）。
func BuildOpenAIBPSUpstreamRequest(ctx context.Context, account *Account, body []byte, stream bool) (*http.Request, error) {
	if account == nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "bps_invalid_account",
			"OpenAI BPS account is missing")
	}
	if !account.IsOpenAIBPS() {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "bps_invalid_account",
			"account %d is not an OpenAI BPS account", account.ID)
	}
	if account.Type != AccountTypeOAuth {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "bps_invalid_account",
			"OpenAI BPS account %d must be an oauth account, got %q", account.ID, account.Type)
	}
	token := openAIBPSAccessToken(account)
	workspace := openAIBPSWorkspace(account)
	if token == "" || workspace == "" {
		return nil, infraerrors.Newf(http.StatusUnauthorized, "bps_invalid_credentials",
			"OpenAI BPS account %d credentials are incomplete", account.ID)
	}
	if strings.ContainsAny(token+workspace, "\r\n") {
		return nil, infraerrors.Newf(http.StatusUnauthorized, "bps_invalid_credentials",
			"OpenAI BPS account %d credentials contain illegal characters", account.ID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OpenAIBPSUpstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai bps: build upstream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(openAIBPSAccountIDHeader, workspace)
	req.Header.Set(openAIBPSCompatAccountIDHeader, workspace)
	req.Header.Set(OpenAIBPSAuthModeHeader, OpenAIBPSAuthModeChatGPT)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	// SSE 必须避免中间层压缩缓冲，否则事件会被攒成整块才到客户端。
	req.Header.Set("Accept-Encoding", "identity")
	return req, nil
}

// ForwardOpenAIBPSResponse 把上游响应原样转给客户端。
//
//   - JSON：状态码、Content-Type、响应体原样回写；
//   - SSE：按事件边界逐事件转发并 flush；上游在 response.completed 之前断流时
//     向客户端补一个 response.failed 终止事件，绝不伪造 response.completed；
//   - x-request-id 透传给客户端（排障用）；
//   - 非 2xx 一律按原状态码/原始错误体透传（不在这里换号或改写语义）。
//
// 返回 error 仅表示「本地读写失败」（客户端断开/上游读错误已处理为 failed 事件
// 时返回 nil）。handler 在收到 error 后不得再写 JSON 错误体（响应可能已提交）。
func ForwardOpenAIBPSResponse(c *gin.Context, resp *http.Response) error {
	if c == nil || c.Writer == nil {
		return errors.New("openai bps: forward response: gin context is missing")
	}
	if resp == nil {
		return errors.New("openai bps: forward response: upstream response is missing")
	}
	if resp.Body == nil {
		return errors.New("openai bps: forward response: upstream body is missing")
	}
	defer func() { _ = resp.Body.Close() }()
	requestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	if requestID != "" {
		c.Header("x-request-id", requestID)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	lowerContentType := strings.ToLower(contentType)

	// 上游错误：状态码与响应体原样透传。BPS 的 4xx/5xx 错误体已经是 OpenAI
	// 兼容错误信封，网关不再包装（否则客户端拿不到上游错误码）。
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(openAIBPSMaxJSONResponseBytes)+1))
		if err != nil {
			return fmt.Errorf("openai bps: read upstream error body: %w", err)
		}
		if len(data) > openAIBPSMaxJSONResponseBytes {
			return fmt.Errorf("openai bps: upstream error body exceeds %d bytes", openAIBPSMaxJSONResponseBytes)
		}
		if contentType != "" {
			c.Header("Content-Type", contentType)
		}
		c.Status(resp.StatusCode)
		_, err = c.Writer.Write(data)
		return err
	}

	if strings.Contains(lowerContentType, "text/event-stream") {
		return forwardOpenAIBPSSSE(c, resp)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(openAIBPSMaxJSONResponseBytes)+1))
	if err != nil {
		return fmt.Errorf("openai bps: read upstream json body: %w", err)
	}
	if len(data) > openAIBPSMaxJSONResponseBytes {
		return fmt.Errorf("openai bps: upstream json body exceeds %d bytes", openAIBPSMaxJSONResponseBytes)
	}
	if contentType == "" {
		contentType = "application/json"
	}
	c.Header("Content-Type", contentType)
	c.Status(resp.StatusCode)
	_, err = c.Writer.Write(data)
	return err
}

// forwardOpenAIBPSSSE 逐事件转发 SSE。
func forwardOpenAIBPSSSE(c *gin.Context, resp *http.Response) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), openAIBPSMaxSSELineBytes)

	var (
		eventName  string
		dataLines  []string
		eventBytes int
	)
	// dispatch 在空行边界判定事件类型；返回 true 表示已到达终止事件，转发结束。
	dispatch := func() (bool, error) {
		typ := openAIBPSSSEEventType(eventName, dataLines)
		eventName = ""
		dataLines = nil
		eventBytes = 0
		flush()
		return openAIBPSTerminalSSEEvent(typ), nil
	}
	writeLine := func(line string) error {
		if _, err := c.Writer.Write([]byte(line + "\n")); err != nil {
			return fmt.Errorf("openai bps: write sse line: %w", err)
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if err := writeLine(line); err != nil {
			return err
		}
		if line == "" {
			terminal, err := dispatch()
			if err != nil {
				return err
			}
			if terminal {
				// response.completed / response.failed 之后不再等待 EOF：
				// 上游可能保持连接，继续读会把请求生命周期拖到超时。
				return nil
			}
			continue
		}
		eventBytes += len(line) + 1
		if eventBytes > openAIBPSMaxSSEEventBytes {
			return fmt.Errorf("openai bps: sse event exceeds %d bytes", openAIBPSMaxSSEEventBytes)
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			dataLines = append(dataLines, data)
		}
	}

	readErr := scanner.Err()
	// EOF 时可能还有未以空行结束的尾事件：先按事件语义判定，避免把已经
	// 到达的 response.completed 误判成断流。
	if eventBytes > 0 || len(dataLines) > 0 {
		if err := writeLine(""); err != nil {
			return err
		}
		terminal, err := dispatch()
		if err != nil {
			return err
		}
		if terminal {
			return nil
		}
	}
	if readErr != nil {
		logger.FromContext(openAIBPSContext(c)).Warn("openai bps: upstream sse read failed before terminal event",
			zap.Error(readErr))
	}
	if err := writeOpenAIBPSStreamFailure(c, openAIBPSStreamInterruptedCode,
		"BPS upstream stream ended before response.completed"); err != nil {
		return err
	}
	flush()
	return nil
}

func openAIBPSContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		return c.Request.Context()
	}
	return context.Background()
}

// openAIBPSSSEEventType 从 event: 行与 data: JSON 中取事件类型；data 里的
// type 优先（OpenAI Responses 事件总是携带 type）。
func openAIBPSSSEEventType(eventName string, dataLines []string) string {
	if len(dataLines) > 0 {
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		if payload != "" && payload != "[DONE]" {
			if typ := strings.TrimSpace(gjson.Get(payload, "type").String()); typ != "" {
				return typ
			}
		}
	}
	return strings.TrimSpace(eventName)
}

// openAIBPSTerminalSSEEvent 报告事件是否是终止事件。终止事件之后网关不再
// 转发后续字节，也不会再补 response.failed。
func openAIBPSTerminalSSEEvent(typ string) bool {
	switch typ {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "error":
		return true
	default:
		return false
	}
}

// writeOpenAIBPSStreamFailure 在已开始的 SSE 流上补一个 response.failed 终止
// 事件。只允许用于「上游没有给出终止事件」的场景；已收到 completed 的流绝不
// 调用（伪造 completed 反向同理：绝不把断流写成 completed）。
func writeOpenAIBPSStreamFailure(c *gin.Context, code, message string) error {
	payload := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"object": "response",
			"status": "failed",
			"error": map[string]any{
				"type":    "server_error",
				"code":    code,
				"message": message,
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("openai bps: encode stream failure: %w", err)
	}
	if _, err := fmt.Fprintf(c.Writer, "event: response.failed\ndata: %s\n\n", data); err != nil {
		return fmt.Errorf("openai bps: write stream failure: %w", err)
	}
	return nil
}
