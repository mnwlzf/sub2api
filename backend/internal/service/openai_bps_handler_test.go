package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func enableOpenAIBPSForTest(t *testing.T) {
	t.Helper()
	previous := viper.GetBool(config.OpenAIBPSConfigKeyEnabled)
	t.Cleanup(func() { viper.Set(config.OpenAIBPSConfigKeyEnabled, previous) })
	viper.Set(config.OpenAIBPSConfigKeyEnabled, true)
}

func disableOpenAIBPSForTest(t *testing.T) {
	t.Helper()
	previous := viper.GetBool(config.OpenAIBPSConfigKeyEnabled)
	t.Cleanup(func() { viper.Set(config.OpenAIBPSConfigKeyEnabled, previous) })
	viper.Set(config.OpenAIBPSConfigKeyEnabled, false)
}

func newBPSHandlerTestContext(t *testing.T, body string, headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		c.Request.Header.Set(key, value)
	}
	c.Set("api_key", &APIKey{ID: 42})
	return c, recorder
}

func newBPSUpstreamStub(t *testing.T, handler func(*http.Request) (*http.Response, error)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(handler)}
}

func decodeOpenAIBPSAPIError(t *testing.T, recorder *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload), "response body: %s", recorder.Body.String())
	require.NotEmpty(t, payload.Error.Type)
	return payload.Error.Code, payload.Error.Message
}

// bpsTestResolver 同时实现 Select / Lookup / Release 三个契约，用于验证
// 已绑定会话按 ID 精确取号 + 租约释放。
type bpsTestResolver struct {
	mu          sync.Mutex
	selectFn    func(ctx context.Context, c *gin.Context, model string) (*Account, error)
	lookupFn    func(ctx context.Context, accountID int64) (*Account, error)
	selectCalls int
	lookupCalls int
	released    []int64
}

func (r *bpsTestResolver) SelectOpenAIBPSAccount(ctx context.Context, c *gin.Context, model string) (*Account, error) {
	r.mu.Lock()
	r.selectCalls++
	r.mu.Unlock()
	if r.selectFn == nil {
		return nil, errors.New("no account")
	}
	return r.selectFn(ctx, c, model)
}

func (r *bpsTestResolver) OpenAIBPSAccountByID(ctx context.Context, accountID int64) (*Account, error) {
	r.mu.Lock()
	r.lookupCalls++
	r.mu.Unlock()
	if r.lookupFn == nil {
		return nil, errors.New("account not found")
	}
	return r.lookupFn(ctx, accountID)
}

func (r *bpsTestResolver) ReleaseOpenAIBPSAccount(_ context.Context, account *Account) {
	if account == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, account.ID)
}

func (r *bpsTestResolver) counts() (selectCalls, lookupCalls int, released []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int64, len(r.released))
	copy(out, r.released)
	return r.selectCalls, r.lookupCalls, out
}

// bpsSelectOnlyResolver 只实现 Select：用于验证「无 Lookup 扩展时把绑定写进
// gin 上下文并强校验」的退化路径。
type bpsSelectOnlyResolver struct {
	selectFn func(ctx context.Context, c *gin.Context, model string) (*Account, error)
}

func (r *bpsSelectOnlyResolver) SelectOpenAIBPSAccount(ctx context.Context, c *gin.Context, model string) (*Account, error) {
	return r.selectFn(ctx, c, model)
}

func TestOpenAIBPSHandlerPlatformDisabled(t *testing.T) {
	disableOpenAIBPSForTest(t)
	upstreamCalls := 0
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			upstreamCalls++
			return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
		}),
	})
	c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":"hello"}`, nil)
	h.Responses(c)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	code, _ := decodeOpenAIBPSAPIError(t, recorder)
	require.Equal(t, OpenAIBPSErrorPlatformDisabled, code)
	require.Zero(t, upstreamCalls, "平台关闭时不得触碰上游")
}

// 端到端（HTTP client 打桩）验证：客户端认证头不进入上游、请求头正确注入、
// JSON 原样回写、绑定落库。
func TestOpenAIBPSHandlerJSONForwardAndHeaderInjection(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	body := `{"model":"gpt-6-astra","input":"hello bps"}`
	upstreamBody := `{"id":"resp_1","object":"response","status":"completed","output":[]}`
	// 阶段 4 起响应会经过转换：model 还原为客户端请求的模型名。
	transformedBody := `{"id":"resp_1","model":"gpt-6-astra","object":"response","status":"completed","output":[]}`
	var captured *http.Request
	var capturedBody []byte
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "secret-token-7", "ws-7"), nil
		}},
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(req *http.Request) (*http.Response, error) {
			captured = req
			capturedBody, _ = io.ReadAll(req.Body)
			return bpsTestResponse(http.StatusOK, "application/json", "req_up_json", upstreamBody), nil
		}),
	})
	c, recorder := newBPSHandlerTestContext(t, body, map[string]string{
		"Authorization": "Bearer client-api-key",
		"x-api-key":     "client-api-key",
		"Cookie":        "session=client-cookie",
		"User-Agent":    "bps-client/1.0",
	})
	h.Responses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, transformedBody, recorder.Body.String())
	require.Equal(t, "req_up_json", recorder.Header().Get("x-request-id"))
	require.NotNil(t, captured)
	require.Equal(t, OpenAIBPSUpstreamURL, captured.URL.String())
	require.Equal(t, http.MethodPost, captured.Method)
	require.Equal(t, "Bearer secret-token-7", captured.Header.Get("Authorization"))
	require.Equal(t, "ws-7", captured.Header.Get("chatgpt-account-id"))
	require.Equal(t, "ws-7", captured.Header.Get("x-openai-account-id"))
	require.Equal(t, OpenAIBPSAuthModeChatGPT, captured.Header.Get(OpenAIBPSAuthModeHeader))
	require.Equal(t, "application/json", captured.Header.Get("Content-Type"))
	require.Equal(t, "application/json", captured.Header.Get("Accept"))
	// 客户端认证头绝不进入上游。
	require.Empty(t, captured.Header.Get("x-api-key"))
	require.Empty(t, captured.Header.Get("Cookie"))
	require.NotEqual(t, "Bearer client-api-key", captured.Header.Get("Authorization"))
	// 上游 body 是协议翻译结果：客户端 tools 声明不再直发上游，工具目录写进
	// developer 协议消息，store 固定 false。
	require.False(t, gjson.GetBytes(capturedBody, "tools").Exists())
	require.False(t, gjson.GetBytes(capturedBody, "store").Bool())
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(capturedBody, "model").String())
	require.Equal(t, "explicit", gjson.GetBytes(capturedBody, "model_selection").String())
	inputItems := gjson.GetBytes(capturedBody, "input").Array()
	require.Len(t, inputItems, 2)
	require.Equal(t, "developer", inputItems[0].Get("role").String())
	require.Contains(t, inputItems[0].Get("content.0.text").String(), "external Responses client")
	require.Equal(t, "user", inputItems[1].Get("role").String())

	// 未绑定会话在首次请求时落库，且绑定内容与账号一致。
	sessionKey, explicit := OpenAIBPSSessionKey(42, []byte(body))
	require.False(t, explicit)
	require.NotEmpty(t, sessionKey)
	accountID, workspace, ok, err := store.BoundAccount(context.Background(), 42, sessionKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(7), accountID)
	require.Equal(t, "ws-7", workspace)

	// 观测键：Lead 的计费/指标接线点。
	require.Equal(t, int64(7), c.GetInt64(OpenAIBPSAccountIDContextKey))
	require.Equal(t, sessionKey, c.GetString(OpenAIBPSSessionKeyContextKey))
	require.Equal(t, http.StatusOK, c.GetInt(OpenAIBPSUpstreamStatusContextKey))
	require.Equal(t, "req_up_json", c.GetString(OpenAIBPSUpstreamRequestIDContextKey))
}

func TestOpenAIBPSHandlerStreamForward(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	messageItem := `{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}`
	sse := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":` + messageItem + `}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-6-astra","output":[` + messageItem + `]}}` + "\n\n"
	var captured *http.Request
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}},
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(req *http.Request) (*http.Response, error) {
			captured = req
			return bpsTestResponse(http.StatusOK, "text/event-stream", "req_up_sse", sse), nil
		}),
	})
	c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","stream":true,"input":"hello"}`, nil)
	h.Responses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.output_text.delta")
	require.Contains(t, forwarded, "event: response.completed")
	require.NotContains(t, forwarded, "response.failed")
	require.Contains(t, forwarded, `"text":"hello"`)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Equal(t, "req_up_sse", recorder.Header().Get("x-request-id"))
	require.NotNil(t, captured)
	require.Equal(t, "text/event-stream", captured.Header.Get("Accept"))
}

// 已绑定会话：必须使用绑定账号（按 ID 精确取号），不重新调度。
func TestOpenAIBPSHandlerBoundSessionUsesBoundAccount(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	sessionKey, _ := OpenAIBPSSessionKey(42, []byte(`{"model":"gpt-6-astra","session_id":"conv-bound","input":"hi"}`))
	created, err := store.Bind(ctx, 42, sessionKey, 7, "ws-bound")
	require.NoError(t, err)
	require.True(t, created)

	resolver := &bpsTestResolver{
		selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(99, "token-99", "ws-99"), nil
		},
		lookupFn: func(_ context.Context, accountID int64) (*Account, error) {
			require.Equal(t, int64(7), accountID)
			return bpsTestAccount(7, "token-7", "ws-bound"), nil
		},
	}
	var authHeader string
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: resolver,
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(req *http.Request) (*http.Response, error) {
			authHeader = req.Header.Get("Authorization")
			return bpsTestResponse(http.StatusOK, "application/json", "", `{"id":"resp_1","status":"completed","output":[]}`), nil
		}),
	})
	c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","session_id":"conv-bound","input":"hi"}`, nil)
	h.Responses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer token-7", authHeader)
	selectCalls, lookupCalls, _ := resolver.counts()
	require.Zero(t, selectCalls, "已绑定会话不得重新调度")
	require.Equal(t, 1, lookupCalls)
}

// 已绑定账号不可用（删除/禁用/凭据过期/取号失败/workspace 漂移）：返回
// bps_account_unavailable，绝不静默换号。
func TestOpenAIBPSHandlerBoundAccountUnavailable(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	body := `{"model":"gpt-6-astra","prompt_cache_key":"conv-dead","input":"hi"}`
	sessionKey, _ := OpenAIBPSSessionKey(42, []byte(body))
	_, err := store.Bind(ctx, 42, sessionKey, 7, "ws-dead")
	require.NoError(t, err)

	run := func(t *testing.T, lookup func(context.Context, int64) (*Account, error)) (*httptest.ResponseRecorder, *int) {
		t.Helper()
		upstreamCalls := 0
		h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
			Resolver: &bpsTestResolver{
				selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
					return bpsTestAccount(99, "token-99", "ws-99"), nil
				},
				lookupFn: lookup,
			},
			Sessions: store,
			HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
				upstreamCalls++
				return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
			}),
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		return recorder, &upstreamCalls
	}
	requireUnavailable := func(t *testing.T, recorder *httptest.ResponseRecorder, upstreamCalls *int) {
		t.Helper()
		require.Equal(t, http.StatusConflict, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorAccountUnavailable, code)
		require.Zero(t, *upstreamCalls, "绑定不可用时不得触碰上游")
	}

	t.Run("账号被禁用", func(t *testing.T) {
		disabled := bpsTestAccount(7, "token-7", "ws-dead")
		disabled.Status = StatusDisabled
		recorder, calls := run(t, func(context.Context, int64) (*Account, error) { return disabled, nil })
		requireUnavailable(t, recorder, calls)
	})

	t.Run("账号被删除（取号失败）", func(t *testing.T) {
		recorder, calls := run(t, func(context.Context, int64) (*Account, error) {
			return nil, errors.New("account not found")
		})
		requireUnavailable(t, recorder, calls)
	})

	t.Run("workspace 漂移", func(t *testing.T) {
		recorder, calls := run(t, func(context.Context, int64) (*Account, error) {
			return bpsTestAccount(7, "token-7", "ws-other"), nil
		})
		requireUnavailable(t, recorder, calls)
	})

	t.Run("凭据缺失", func(t *testing.T) {
		recorder, calls := run(t, func(context.Context, int64) (*Account, error) {
			return bpsTestAccount(7, "", "ws-dead"), nil
		})
		requireUnavailable(t, recorder, calls)
	})
}

// 绑定并发冲突：本请求 Bind 失败（别的请求先绑定了 7），必须重读绑定并以
// 绑定账号为准，不能覆盖赢家；先前选中的账号租约要释放。
func TestOpenAIBPSHandlerBindConflictRereadsWinner(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	resolver := &bpsTestResolver{
		lookupFn: func(_ context.Context, accountID int64) (*Account, error) {
			require.Equal(t, int64(7), accountID)
			return bpsTestAccount(7, "token-winner", "ws-winner"), nil
		},
	}
	// 模拟并发：在调度选号返回之前，另一个请求已经把会话绑定到账号 7。
	resolver.selectFn = func(ctx context.Context, c *gin.Context, _ string) (*Account, error) {
		sessionKey := c.GetString(OpenAIBPSSessionKeyContextKey)
		require.NotEmpty(t, sessionKey)
		created, err := store.Bind(ctx, 42, sessionKey, 7, "ws-winner")
		require.NoError(t, err)
		require.True(t, created)
		return bpsTestAccount(9, "token-loser", "ws-loser"), nil
	}
	var authHeader string
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: resolver,
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(req *http.Request) (*http.Response, error) {
			authHeader = req.Header.Get("Authorization")
			return bpsTestResponse(http.StatusOK, "application/json", "", `{"id":"resp_1","status":"completed","output":[]}`), nil
		}),
	})
	c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":"conflict"}`, nil)
	h.Responses(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer token-winner", authHeader, "并发冲突后必须使用赢家绑定账号")
	_, lookupCalls, released := resolver.counts()
	require.Equal(t, 1, lookupCalls)
	require.Contains(t, released, int64(9), "被放弃的选中账号租约必须释放")

	sessionKey, _ := OpenAIBPSSessionKey(42, []byte(`{"model":"gpt-6-astra","input":"conflict"}`))
	accountID, workspace, ok, err := store.BoundAccount(context.Background(), 42, sessionKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(7), accountID)
	require.Equal(t, "ws-winner", workspace)
}

// 没有 Lookup 扩展时的退化路径：绑定约束通过 gin 上下文交给 Resolver，
// 选择后强校验；Resolver 忽略绑定则请求失败而不是换号。
func TestOpenAIBPSHandlerBoundSessionFallbackWithoutLookup(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	body := `{"model":"gpt-6-astra","session_id":"conv-fallback","input":"hi"}`
	sessionKey, _ := OpenAIBPSSessionKey(42, []byte(body))
	_, err := store.Bind(ctx, 42, sessionKey, 7, "ws-7")
	require.NoError(t, err)

	t.Run("Resolver 遵守绑定", func(t *testing.T) {
		resolver := &bpsSelectOnlyResolver{selectFn: func(_ context.Context, c *gin.Context, _ string) (*Account, error) {
			require.Equal(t, int64(7), c.GetInt64(OpenAIBPSBoundAccountIDContextKey))
			require.Equal(t, "ws-7", c.GetString(OpenAIBPSBoundWorkspaceContextKey))
			return bpsTestAccount(7, "token-7", "ws-7"), nil
		}}
		var authHeader string
		h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
			Resolver: resolver,
			Sessions: store,
			HTTPClient: newBPSUpstreamStub(t, func(req *http.Request) (*http.Response, error) {
				authHeader = req.Header.Get("Authorization")
				return bpsTestResponse(http.StatusOK, "application/json", "", `{"id":"resp_1","status":"completed","output":[]}`), nil
			}),
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "Bearer token-7", authHeader)
	})

	t.Run("Resolver 忽略绑定", func(t *testing.T) {
		upstreamCalls := 0
		resolver := &bpsSelectOnlyResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return bpsTestAccount(9, "token-9", "ws-9"), nil
		}}
		h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
			Resolver: resolver,
			Sessions: store,
			HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
				upstreamCalls++
				return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
			}),
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusConflict, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorAccountUnavailable, code)
		require.Zero(t, upstreamCalls)
	})
}

func TestOpenAIBPSSessionKeyDerivation(t *testing.T) {
	explicitBodies := []string{
		`{"model":"m","session_id":"conv-1","input":"hello"}`,
		`{"model":"m","sessionId":"conv-1","input":"hello"}`,
		`{"model":"m","prompt_cache_key":"conv-1","input":"hello"}`,
		`{"model":"m","promptCacheKey":"conv-1","input":"hello"}`,
		`{"model":"m","client_metadata":{"session_id":"conv-1"},"input":"hello"}`,
	}
	var explicitKey string
	for i, body := range explicitBodies {
		key, explicit := OpenAIBPSSessionKey(42, []byte(body))
		require.True(t, explicit, "body %d 应识别为显式会话", i)
		require.NotEmpty(t, key)
		if i == 0 {
			explicitKey = key
		} else {
			require.Equal(t, explicitKey, key, "同一会话 ID 的不同字段形态必须派生同一键")
		}
	}

	// 首条用户消息派生：input 字符串与 content 分片两种形态一致。
	stringBody := `{"model":"m","input":"hello world"}`
	partsBody := `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}]}`
	keyA, explicitA := OpenAIBPSSessionKey(42, []byte(stringBody))
	keyB, explicitB := OpenAIBPSSessionKey(42, []byte(partsBody))
	require.False(t, explicitA)
	require.False(t, explicitB)
	require.NotEmpty(t, keyA)
	require.NotEqual(t, keyA, keyB, "文本不同派生键必须不同")
	require.Equal(t, keyA, mustOpenAIBPSSessionKey(t, 42, stringBody))

	// 同一段会话 ID 在不同 API Key 下必须是不同键。
	otherTenant, _ := OpenAIBPSSessionKey(43, []byte(explicitBodies[0]))
	require.NotEqual(t, explicitKey, otherTenant)

	// 显式 ID 优先于首条用户消息。
	explicitBody := `{"model":"m","session_id":"conv-x","input":"hello"}`
	keyC, explicitC := OpenAIBPSSessionKey(42, []byte(explicitBody))
	require.True(t, explicitC)
	require.NotEqual(t, keyC, keyA)

	// 无法派生：空 input、只有 assistant 消息、纯工具结果。
	for _, body := range []string{
		`{"model":"m","input":[]}`,
		`{"model":"m"}`,
		`{"model":"m","input":[{"role":"assistant","content":"hi"}]}`,
		`{"model":"m","input":[{"type":"function_call_output","call_id":"c1","output":"x"}]}`,
	} {
		key, explicit := OpenAIBPSSessionKey(42, []byte(body))
		require.Empty(t, key)
		require.False(t, explicit)
	}
}

func mustOpenAIBPSSessionKey(t *testing.T, apiKeyID int64, body string) string {
	t.Helper()
	key, _ := OpenAIBPSSessionKey(apiKeyID, []byte(body))
	return key
}

func TestOpenAIBPSHandlerInvalidRequests(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	upstreamCalls := 0
	newHandler := func() *OpenAIBPSHandler {
		return NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
			Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			}},
			Sessions: store,
			HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
				upstreamCalls++
				return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
			}),
		})
	}

	t.Run("缺少 model", func(t *testing.T) {
		c, recorder := newBPSHandlerTestContext(t, `{"input":"hi"}`, nil)
		newHandler().Responses(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorInvalidRequest, code)
	})

	t.Run("空 body", func(t *testing.T) {
		c, recorder := newBPSHandlerTestContext(t, "   ", nil)
		newHandler().Responses(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("无法派生会话身份的新会话", func(t *testing.T) {
		c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":[]}`, nil)
		newHandler().Responses(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorInvalidRequest, code)
	})

	t.Run("无身份的续写请求", func(t *testing.T) {
		c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":[{"role":"assistant","content":"hi"}]}`, nil)
		newHandler().Responses(c)
		require.Equal(t, http.StatusConflict, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorContextExpired, code)
	})

	t.Run("缺少 API Key 上下文", func(t *testing.T) {
		c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
		c.Set("api_key", nil)
		newHandler().Responses(c)
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorUnauthorized, code)
	})

	require.Zero(t, upstreamCalls, "所有非法请求都不得触碰上游")
}

func TestOpenAIBPSHandlerUpstreamFailures(t *testing.T) {
	enableOpenAIBPSForTest(t)
	// 每个子测试独立会话存储：失败路径不依赖前一个子测试留下的绑定。
	newHandler := func(t *testing.T, client *http.Client, resolver *bpsTestResolver) *OpenAIBPSHandler {
		store, _ := newOpenAIBPSTestStore(t)
		if resolver == nil {
			resolver = &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			}}
		}
		return NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{Resolver: resolver, Sessions: store, HTTPClient: client})
	}
	body := `{"model":"gpt-6-astra","input":"hi"}`

	t.Run("上游 429 原样透传", func(t *testing.T) {
		upstreamError := `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
		h := newHandler(t, newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusTooManyRequests, "application/json", "req_up_429", upstreamError), nil
		}), nil)
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusTooManyRequests, recorder.Code)
		require.JSONEq(t, upstreamError, recorder.Body.String())
		require.Equal(t, "req_up_429", recorder.Header().Get("x-request-id"))
	})

	t.Run("上游网络错误", func(t *testing.T) {
		h := newHandler(t, newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		}), nil)
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorUpstream, code)
	})

	t.Run("Resolver 泛化错误", func(t *testing.T) {
		h := newHandler(t, newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
		}), &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return nil, errors.New("no bps account")
		}})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorAccountUnavailable, code)
	})

	t.Run("Resolver 显式错误码透传", func(t *testing.T) {
		h := newHandler(t, newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
		}), &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
			return nil, infraerrors.Newf(http.StatusTooManyRequests, "bps_account_cooling", "all BPS accounts are cooling down")
		}})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusTooManyRequests, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, "bps_account_cooling", code)
	})

	t.Run("会话存储不可用", func(t *testing.T) {
		h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
			Resolver: &bpsTestResolver{selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			}},
			Sessions: NewOpenAIBPSSessionStore(nil),
			HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
				return bpsTestResponse(http.StatusOK, "application/json", "", `{}`), nil
			}),
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorStateUnavailable, code)
	})
}

func TestRegisterOpenAIBPSRoutes(t *testing.T) {
	disableOpenAIBPSForTest(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/v1")
	RegisterOpenAIBPSRoutes(v1, NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "路由必须已注册且平台关闭时返回明确错误")
	code, _ := decodeOpenAIBPSAPIError(t, recorder)
	require.Equal(t, OpenAIBPSErrorPlatformDisabled, code)

	// 其它方法未注册。
	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Equal(t, http.StatusNotFound, getRecorder.Code)

	// nil 路由组/处理器安全。
	RegisterOpenAIBPSRoutes(nil, nil)
}
