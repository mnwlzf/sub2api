package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// bpsUsageRecorderStub 记录 RecordUsage 调用，用于验证 BPS 计费接线。
type bpsUsageRecorderStub struct {
	mu     sync.Mutex
	inputs []*OpenAIRecordUsageInput
	err    error
}

func (r *bpsUsageRecorderStub) RecordUsage(_ context.Context, input *OpenAIRecordUsageInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	return r.err
}

func (r *bpsUsageRecorderStub) recorded() []*OpenAIRecordUsageInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*OpenAIRecordUsageInput, len(r.inputs))
	copy(out, r.inputs)
	return out
}

// newBPSBillingTestContext 在通用测试上下文上补齐计费所需的用户/分组信息。
func newBPSBillingTestContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	c.Set("api_key", &APIKey{
		ID:      42,
		User:    &User{ID: 9},
		GroupID: bpsInt64Ptr(3),
		Group:   &Group{ID: 3, Platform: PlatformOpenAIBPS},
	})
	return c, recorder
}

func bpsInt64Ptr(value int64) *int64 { return &value }

func TestOpenAIBPSUsageFromJSON(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantOK bool
		want   OpenAIBPSUsage
	}{
		{
			name:   "responses usage",
			body:   `{"usage":{"input_tokens":120,"output_tokens":30,"total_tokens":150,"input_tokens_details":{"cached_tokens":64},"output_tokens_details":{"reasoning_tokens":8}}}`,
			wantOK: true,
			want: OpenAIBPSUsage{
				InputTokens: 120, OutputTokens: 30, TotalTokens: 150,
				CacheReadInputTokens: 64, ReasoningOutputTokens: 8,
			},
		},
		{
			name:   "cache creation and chat fallback",
			body:   `{"usage":{"prompt_tokens":10,"completion_tokens":2,"cache_creation_input_tokens":5}}`,
			wantOK: true,
			want:   OpenAIBPSUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12, CacheCreationInputTokens: 5},
		},
		{
			name:   "wrapped response usage",
			body:   `{"response":{"usage":{"input_tokens":6,"output_tokens":2}}}`,
			wantOK: true,
			want:   OpenAIBPSUsage{InputTokens: 6, OutputTokens: 2, TotalTokens: 8},
		},
		{
			name:   "missing usage",
			body:   `{"id":"resp_1"}`,
			wantOK: false,
		},
		{
			name:   "null usage",
			body:   `{"usage":null}`,
			wantOK: false,
		},
		{
			name:   "non object usage",
			body:   `{"usage":12}`,
			wantOK: false,
		},
		{
			name:   "invalid json",
			body:   `{`,
			wantOK: false,
		},
		{
			name:   "negative values clamped",
			body:   `{"usage":{"input_tokens":-5,"output_tokens":-1}}`,
			wantOK: true,
			want:   OpenAIBPSUsage{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := OpenAIBPSUsageFromJSON([]byte(tt.body))
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestOpenAIBPSUsageFromValue(t *testing.T) {
	raw := map[string]any{
		"input_tokens":  float64(7),
		"output_tokens": float64(2),
	}
	usage, ok := OpenAIBPSUsageFromValue(raw)
	require.True(t, ok)
	require.Equal(t, 7, usage.InputTokens)
	require.Equal(t, 2, usage.OutputTokens)

	_, ok = OpenAIBPSUsageFromValue(nil)
	require.False(t, ok)
}

func TestBuildOpenAIBPSRecordUsageInput(t *testing.T) {
	apiKey := &APIKey{ID: 42, User: &User{ID: 9}, GroupID: bpsInt64Ptr(3), Group: &Group{ID: 3}}
	account := bpsTestAccount(7, "token", "workspace")
	pricingAt := time.Unix(1_700_000_000, 0)
	input := BuildOpenAIBPSRecordUsageInput(OpenAIBPSUsageRecord{
		APIKey:        apiKey,
		Account:       account,
		Model:         "gpt-6-astra",
		UpstreamModel: "gpt-6-astra-upstream",
		RequestID:     "req_1",
		ResponseID:    "resp_1",
		Usage: OpenAIBPSUsage{
			InputTokens:          100,
			OutputTokens:         20,
			CacheReadInputTokens: 40,
		},
		Stream:             true,
		Duration:           2 * time.Second,
		InboundEndpoint:    OpenAIBPSInboundEndpointResponses,
		UpstreamEndpoint:   OpenAIBPSUpstreamEndpointPath,
		UserAgent:          "bps-client/1.0",
		IPAddress:          "203.0.113.7",
		SessionID:          "session-1",
		RequestPayloadHash: "hash-1",
		QuotaPlatform:      PlatformOpenAIBPS,
		PricingAt:          pricingAt,
	})
	require.NotNil(t, input)
	require.Same(t, apiKey, input.APIKey)
	require.Equal(t, apiKey.User, input.User, "User 缺省时回退 APIKey.User")
	require.Same(t, account, input.Account)
	require.Equal(t, "gpt-6-astra", input.Result.Model)
	require.Equal(t, "gpt-6-astra-upstream", input.Result.UpstreamModel)
	require.Equal(t, "req_1", input.Result.RequestID)
	require.Equal(t, "resp_1", input.Result.ResponseID)
	require.True(t, input.Result.Stream)
	require.Equal(t, 2*time.Second, input.Result.Duration)
	require.Equal(t, 100, input.Result.Usage.InputTokens)
	require.Equal(t, 20, input.Result.Usage.OutputTokens)
	require.Equal(t, 40, input.Result.Usage.CacheReadInputTokens)
	require.Equal(t, OpenAIBPSInboundEndpointResponses, input.InboundEndpoint)
	require.Equal(t, OpenAIBPSUpstreamEndpointPath, input.UpstreamEndpoint)
	require.Equal(t, pricingAt, input.PricingAt)
	require.Equal(t, PlatformOpenAIBPS, input.QuotaPlatform)
	require.Equal(t, "gpt-6-astra", input.OriginalModel)
	require.Equal(t, "gpt-6-astra-upstream", input.ChannelMappedModel)
}

func TestBuildOpenAIBPSRecordUsageInputNoMapping(t *testing.T) {
	input := BuildOpenAIBPSRecordUsageInput(OpenAIBPSUsageRecord{
		APIKey:        &APIKey{ID: 42, User: &User{ID: 9}},
		Account:       bpsTestAccount(7, "token", "workspace"),
		Model:         "gpt-6-astra",
		UpstreamModel: "gpt-6-astra",
		RequestID:     "req_1",
	})
	require.Empty(t, input.Result.UpstreamModel, "未映射时不重复写上游模型")
	require.Equal(t, "req_1", input.Result.RequestID)
	require.Equal(t, "gpt-6-astra", input.ChannelMappedModel)
}

// 成功 JSON：上游 usage 必须被记录，字段与上游一致。
func TestOpenAIBPSHandlerRecordsUsageOnJSONSuccess(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{}
	upstreamBody := `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":120,"output_tokens":30,"input_tokens_details":{"cached_tokens":64}}}`
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "req_up_1", upstreamBody), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusOK, response.Code)
	inputs := recorder.recorded()
	require.Len(t, inputs, 1)
	got := inputs[0]
	require.Equal(t, 120, got.Result.Usage.InputTokens)
	require.Equal(t, 30, got.Result.Usage.OutputTokens)
	require.Equal(t, 64, got.Result.Usage.CacheReadInputTokens)
	require.Equal(t, "gpt-6-astra", got.Result.Model)
	require.Equal(t, "req_up_1", got.Result.RequestID)
	require.Equal(t, "resp_1", got.Result.ResponseID)
	require.Equal(t, OpenAIBPSInboundEndpointResponses, got.InboundEndpoint)
	require.Equal(t, int64(7), got.Account.ID)
	require.Equal(t, int64(42), got.APIKey.ID)
}

// 成功 SSE：usage 在 response.completed 里，流结束后记录。
func TestOpenAIBPSHandlerRecordsUsageOnStreamSuccess(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{}
	messageItem := `{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}`
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-6-astra","output":[` + messageItem + `],"usage":{"input_tokens":9,"output_tokens":4}}}` + "\n\n"
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "text/event-stream", "req_sse", sse), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","stream":true,"input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusOK, response.Code)
	inputs := recorder.recorded()
	require.Len(t, inputs, 1)
	require.Equal(t, 9, inputs[0].Result.Usage.InputTokens)
	require.Equal(t, 4, inputs[0].Result.Usage.OutputTokens)
	require.True(t, inputs[0].Result.Stream)
	require.Equal(t, "resp_1", inputs[0].Result.ResponseID)
}

// 失败响应仍带 usage：上游 response.failed 事件里的 usage 必须记录。
func TestOpenAIBPSHandlerRecordsUsageOnFailedResponse(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{}
	sse := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_fail","status":"failed","usage":{"input_tokens":15,"output_tokens":1}}}` + "\n\n"
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "text/event-stream", "req_fail", sse), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","stream":true,"input":"hello"}`)
	h.Responses(c)

	require.NotEqual(t, http.StatusOK, response.Code)
	inputs := recorder.recorded()
	require.Len(t, inputs, 1)
	require.Equal(t, 15, inputs[0].Result.Usage.InputTokens)
	require.Equal(t, 1, inputs[0].Result.Usage.OutputTokens)
	require.Equal(t, "resp_fail", inputs[0].Result.ResponseID)
}

// 上游非 2xx 错误体里带 usage（BPS 可能对失败请求计费）：透传错误的同时记录用量。
func TestOpenAIBPSHandlerRecordsUsageFromUpstreamErrorBody(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{}
	errorBody := `{"error":{"code":"basispoints_model_access_changed","message":"model access changed"},"usage":{"input_tokens":5,"output_tokens":0}}`
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusForbidden, "application/json", "req_err", errorBody), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusForbidden, response.Code)
	require.JSONEq(t, errorBody, response.Body.String(), "错误体必须原样透传")
	inputs := recorder.recorded()
	require.Len(t, inputs, 1)
	require.Equal(t, 5, inputs[0].Result.Usage.InputTokens)
}

// SSE 断流：断流前已到达的 usage 仍要记录（即使最终返回 response.failed）。
func TestOpenAIBPSHandlerRecordsUsageOnStreamInterruption(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{}
	// response.in_progress 携带 usage 后上游直接断流，没有终态事件。
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_cut"}}` + "\n\n" +
		"event: response.in_progress\n" +
		`data: {"type":"response.in_progress","response":{"id":"resp_cut","usage":{"input_tokens":33,"output_tokens":7}}}` + "\n\n"
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "text/event-stream", "req_cut", sse), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","stream":true,"input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "response.failed")
	inputs := recorder.recorded()
	require.Len(t, inputs, 1)
	require.Equal(t, 33, inputs[0].Result.Usage.InputTokens)
	require.Equal(t, 7, inputs[0].Result.Usage.OutputTokens)
}

// 没有 usage 时不写 0 token 用量行。
func TestOpenAIBPSHandlerSkipsUsageWithoutUsageField(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "req_no_usage",
				`{"id":"resp_1","status":"completed","output":[]}`), nil
		}),
	})
	c, _ := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Empty(t, recorder.recorded())
}

// 计费资格闸门：返回 ApplicationError 时按该状态终止，且不触达上游。
func TestOpenAIBPSHandlerBillingGateBlocksRequest(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	upstreamCalls := 0
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Billing: bpsBillingGateStub{err: infraerrors.Newf(http.StatusPaymentRequired,
			"insufficient_balance", "insufficient balance")},
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			upstreamCalls++
			return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusPaymentRequired, response.Code)
	code, _ := decodeOpenAIBPSAPIError(t, response)
	require.Equal(t, "insufficient_balance", code)
	require.Zero(t, upstreamCalls)
}

type bpsBillingGateStub struct {
	err error
}

func (s bpsBillingGateStub) CheckOpenAIBPSBillingEligibility(context.Context, *APIKey, *UserSubscription) error {
	return s.err
}

// 默认计费闸门在未接线时必须是 no-op，绝不因 nil 依赖 panic。
func TestOpenAIBPSBillingCacheGateNilSafety(t *testing.T) {
	var gate *OpenAIBPSBillingCacheGate
	require.NoError(t, gate.CheckOpenAIBPSBillingEligibility(context.Background(), nil, nil))
	empty := &OpenAIBPSBillingCacheGate{}
	require.NoError(t, empty.CheckOpenAIBPSBillingEligibility(context.Background(), &APIKey{}, nil))
	require.NoError(t, empty.CheckOpenAIBPSBillingEligibility(context.Background(), &APIKey{User: &User{ID: 1}}, nil))
}

// 结构化计费失败：状态码/错误码/Retry-After 都由适配器给出。
func TestOpenAIBPSHandlerBillingGateStructuredFailure(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Billing: bpsBillingGateStub{err: &OpenAIBPSBillingFailure{
			Status: http.StatusTooManyRequests, Code: "rate_limit_exceeded",
			Message: "API key rate limit exceeded", RetryAfterSeconds: 17,
		}},
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusTooManyRequests, response.Code)
	require.Equal(t, "17", response.Header().Get("Retry-After"))
	code, message := decodeOpenAIBPSAPIError(t, response)
	require.Equal(t, "rate_limit_exceeded", code)
	require.Equal(t, "API key rate limit exceeded", message)
}

// 计费记录失败不能影响已经写回的响应（只记日志）。
func TestOpenAIBPSHandlerUsageRecorderErrorIsNonFatal(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	recorder := &bpsUsageRecorderStub{err: errors.New("billing down")}
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		Usage:    recorder,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "req_1",
				`{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
		}),
	})
	c, response := newBPSBillingTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`)
	h.Responses(c)

	require.Equal(t, http.StatusOK, response.Code)
	require.Len(t, recorder.recorded(), 1)
}

// openAIBPSPeekUpstreamError 必须把 body 还原给转发层（前缀 + 剩余流）。
func TestOpenAIBPSPeekUpstreamErrorRestoresBody(t *testing.T) {
	const body = `{"error":{"code":"model_not_allowed","message":"no access"},"usage":{"input_tokens":3}}`
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	c, _ := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	parsed := openAIBPSPeekUpstreamError(c, resp)
	require.Equal(t, "model_not_allowed", parsed.Code)
	require.Equal(t, "no access", parsed.Message)
	require.True(t, openAIBPSIsModelNotAllowed(parsed.Code, parsed.Message))
	restored, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(restored), "预读后必须还原完整 body")
	require.NoError(t, resp.Body.Close())

	usage, ok := OpenAIBPSUsageFromContext(c)
	require.True(t, ok)
	require.Equal(t, 3, usage.InputTokens)
}

func TestParseOpenAIBPSUpstreamError(t *testing.T) {
	parsed := ParseOpenAIBPSUpstreamError(http.StatusForbidden,
		[]byte(`{"error":{"code":"model_not_allowed","type":"permission_error","message":"model blocked"}}`))
	require.Equal(t, "model_not_allowed", parsed.Code)
	require.Equal(t, "permission_error", parsed.Type)
	require.Equal(t, "model blocked", parsed.Message)

	parsed = ParseOpenAIBPSUpstreamError(http.StatusBadGateway, []byte("upstream exploded"))
	require.Empty(t, parsed.Code)
	require.Equal(t, "upstream exploded", parsed.Message)

	parsed = ParseOpenAIBPSUpstreamError(http.StatusForbidden, []byte(`{"error":{"type":"model_not_allowed"}}`))
	require.Empty(t, parsed.Code)
	require.Equal(t, "model_not_allowed", parsed.DiagnosticCode(), "code 缺失时回退 type 参与分类")

	parsed = ParseOpenAIBPSUpstreamError(http.StatusBadRequest, nil)
	require.Empty(t, parsed.Code)
	require.Empty(t, parsed.Message)
}
