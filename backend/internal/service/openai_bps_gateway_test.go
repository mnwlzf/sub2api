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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// bpsFlushTestWriter 记录 flush 次数，用来断言 SSE 是逐事件刷出的。
type bpsFlushTestWriter struct {
	gin.ResponseWriter
	recorder *httptest.ResponseRecorder
	flushes  int
}

func (w *bpsFlushTestWriter) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}

func (w *bpsFlushTestWriter) WriteString(data string) (int, error) {
	return w.ResponseWriter.WriteString(data)
}

func (w *bpsFlushTestWriter) Flush() {
	w.ResponseWriter.Flush()
	w.flushes++
}

func newBPSForwardTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *bpsFlushTestWriter) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	writer := &bpsFlushTestWriter{ResponseWriter: c.Writer, recorder: recorder}
	c.Writer = writer
	return c, recorder, writer
}

func bpsTestAccount(id int64, token, workspace string) *Account {
	return &Account{
		ID:          id,
		Name:        "bps-test",
		Platform:    PlatformOpenAIBPS,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":       token,
			"chatgpt_account_id": workspace,
		},
	}
}

func bpsTestResponse(status int, contentType, requestID, body string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if requestID != "" {
		header.Set("x-request-id", requestID)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestBuildOpenAIBPSUpstreamRequest(t *testing.T) {
	ctx := context.Background()
	account := bpsTestAccount(7, "secret-token", "workspace-1")
	account.Credentials["base_url"] = "https://evil.example/v1" // 账号级 base_url 必须被忽略
	body := []byte(`{"model":"gpt-6-astra","input":"hello","stream":true}`)

	req, err := BuildOpenAIBPSUpstreamRequest(ctx, account, body, true)
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, OpenAIBPSUpstreamURL, req.URL.String(), "端点固定，不接受账号/客户端覆盖")
	require.Equal(t, "Bearer secret-token", req.Header.Get("Authorization"))
	require.Equal(t, "workspace-1", req.Header.Get("chatgpt-account-id"))
	require.Equal(t, "workspace-1", req.Header.Get("x-openai-account-id"))
	require.Equal(t, OpenAIBPSAuthModeChatGPT, req.Header.Get(OpenAIBPSAuthModeHeader))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	require.Equal(t, "identity", req.Header.Get("Accept-Encoding"))
	// 客户端认证头结构上无法进入上游：函数只接受 body，不复制任何客户端 header。
	require.Empty(t, req.Header.Get("x-api-key"))
	require.Empty(t, req.Header.Get("Cookie"))
	require.Empty(t, req.Header.Get("api-key"))

	sentBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, sentBody)

	streamReq, err := BuildOpenAIBPSUpstreamRequest(ctx, account, body, false)
	require.NoError(t, err)
	require.Equal(t, "application/json", streamReq.Header.Get("Accept"))
}

func TestBuildOpenAIBPSUpstreamRequestRejectsInvalidAccounts(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"model":"gpt-6-astra"}`)

	_, err := BuildOpenAIBPSUpstreamRequest(ctx, nil, body, true)
	require.Error(t, err)

	_, err = BuildOpenAIBPSUpstreamRequest(ctx, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, body, true)
	require.Error(t, err)

	_, err = BuildOpenAIBPSUpstreamRequest(ctx, &Account{ID: 1, Platform: PlatformOpenAIBPS, Type: AccountTypeAPIKey}, body, true)
	require.Error(t, err)

	missingToken := bpsTestAccount(1, "", "workspace")
	_, err = BuildOpenAIBPSUpstreamRequest(ctx, missingToken, body, true)
	require.Error(t, err)

	missingWorkspace := bpsTestAccount(1, "token", "")
	_, err = BuildOpenAIBPSUpstreamRequest(ctx, missingWorkspace, body, true)
	require.Error(t, err)

	headerInjection := bpsTestAccount(1, "token", "workspace")
	headerInjection.Credentials["access_token"] = "token\r\nX-Injected: 1"
	_, err = BuildOpenAIBPSUpstreamRequest(ctx, headerInjection, body, true)
	require.Error(t, err)
}

func TestForwardOpenAIBPSResponseJSON(t *testing.T) {
	upstreamBody := `{"id":"resp_1","object":"response","status":"completed","output":[]}`
	c, recorder, _ := newBPSForwardTestContext(t)
	err := ForwardOpenAIBPSResponse(c, bpsTestResponse(http.StatusOK, "application/json", "req_up_1", upstreamBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, upstreamBody, recorder.Body.String())
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "req_up_1", recorder.Header().Get("x-request-id"))
}

func TestForwardOpenAIBPSResponseUpstreamErrorPassthrough(t *testing.T) {
	errorBody := `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	c, recorder, _ := newBPSForwardTestContext(t)
	err := ForwardOpenAIBPSResponse(c, bpsTestResponse(http.StatusTooManyRequests, "application/json", "req_up_429", errorBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, recorder.Code, "上游状态码必须原样透传")
	require.JSONEq(t, errorBody, recorder.Body.String())
	require.Equal(t, "req_up_429", recorder.Header().Get("x-request-id"))
}

func TestForwardOpenAIBPSResponseSSE(t *testing.T) {
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}` + "\n\n"
	c, recorder, writer := newBPSForwardTestContext(t)
	err := ForwardOpenAIBPSResponse(c, bpsTestResponse(http.StatusOK, "text/event-stream; charset=utf-8", "req_up_sse", sse))
	require.NoError(t, err)
	require.Equal(t, sse, recorder.Body.String(), "SSE 事件按原字节逐事件转发")
	require.GreaterOrEqual(t, writer.flushes, 3, "每个事件边界都必须 flush")
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Equal(t, "req_up_sse", recorder.Header().Get("x-request-id"))
	require.NotContains(t, recorder.Body.String(), "response.failed")
}

// 上游在 response.completed 之前断流：客户端必须收到 response.failed，
// 绝不能把断流伪装成 completed。
func TestForwardOpenAIBPSResponseSSEInterrupted(t *testing.T) {
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n"
	c, recorder, writer := newBPSForwardTestContext(t)
	err := ForwardOpenAIBPSResponse(c, bpsTestResponse(http.StatusOK, "text/event-stream", "", sse))
	require.NoError(t, err)

	forwarded := recorder.Body.String()
	require.True(t, strings.HasPrefix(forwarded, sse), "已到达的事件必须原样转发")
	require.Contains(t, forwarded, "event: response.failed")
	require.NotContains(t, forwarded, "event: response.completed", "不得伪造 completed")

	lastData := forwarded[strings.LastIndex(forwarded, "data: ")+len("data: "):]
	lastData = strings.TrimSpace(lastData)
	var payload struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
			Error  struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal([]byte(lastData), &payload))
	require.Equal(t, "response.failed", payload.Type)
	require.Equal(t, "failed", payload.Response.Status)
	require.Equal(t, openAIBPSStreamInterruptedCode, payload.Response.Error.Code)
	require.GreaterOrEqual(t, writer.flushes, 3)
}

// 上游读完时尾事件没有空行结尾：completed 已到达，不能误判断流。
func TestForwardOpenAIBPSResponseSSETailCompletedWithoutBlankLine(t *testing.T) {
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`
	c, recorder, _ := newBPSForwardTestContext(t)
	err := ForwardOpenAIBPSResponse(c, bpsTestResponse(http.StatusOK, "text/event-stream", "", sse))
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), "event: response.completed")
	require.NotContains(t, recorder.Body.String(), "response.failed")
}

// 上游读错误（非干净 EOF）同样补 response.failed，而不是 completed。
func TestForwardOpenAIBPSResponseSSEReadError(t *testing.T) {
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n"
	body := io.NopCloser(io.MultiReader(strings.NewReader(sse), errReader{err: errors.New("upstream reset")}))
	c, recorder, _ := newBPSForwardTestContext(t)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       body,
	}
	err := ForwardOpenAIBPSResponse(c, resp)
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), "event: response.failed")
	require.NotContains(t, recorder.Body.String(), "event: response.completed")
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestForwardOpenAIBPSResponseNilInputs(t *testing.T) {
	c, _, _ := newBPSForwardTestContext(t)
	require.Error(t, ForwardOpenAIBPSResponse(c, nil))

	c2, _, _ := newBPSForwardTestContext(t)
	require.Error(t, ForwardOpenAIBPSResponse(c2, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}))

	require.Error(t, ForwardOpenAIBPSResponse(nil, bpsTestResponse(http.StatusOK, "application/json", "", "{}")))
}

func TestForwardOpenAIBPSResponseJSONTooLarge(t *testing.T) {
	c, _, _ := newBPSForwardTestContext(t)
	big := strings.Repeat("x", openAIBPSMaxJSONResponseBytes+1)
	err := ForwardOpenAIBPSResponse(c, bpsTestResponse(http.StatusOK, "application/json", "", big))
	require.Error(t, err)
}
