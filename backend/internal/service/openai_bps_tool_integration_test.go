package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 端到端多轮工具往返：第一轮上游请求客户端工具，客户端执行后回传结果，
// 网关从 Redis 恢复上游原始 item 并生成稳定 fc_ ID 的 function_call_output。
func TestOpenAIBPSToolHandlerMultiTurnReplay(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	native := bpsTestNativeCall("call_1", "exec", map[string]any{"command": "ls"})
	message := map[string]any{"type": "message", "id": "msg_2", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}
	var requests []*http.Request
	var bodies [][]byte
	call := 0
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{
			selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
			lookupFn: func(context.Context, int64) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
		},
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			requests = append(requests, req)
			bodies = append(bodies, body)
			call++
			if call == 1 {
				return bpsTestResponse(http.StatusOK, "application/json", "req_1", openAIBPSJSON(bpsTestCompletedResponse(native))), nil
			}
			return bpsTestResponse(http.StatusOK, "application/json", "req_2", openAIBPSJSON(bpsTestCompletedResponse(message))), nil
		}),
	})

	firstBody := `{"model":"gpt-6-astra","session_id":"conv-tools","input":"list files","tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`
	c, recorder := newBPSHandlerTestContext(t, firstBody, nil)
	h.Responses(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var first map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &first))
	output, ok := first["output"].([]any)
	require.True(t, ok)
	require.Len(t, output, 1)
	client, ok := output[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call", client["type"])
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "call_1", client["call_id"])
	require.Equal(t, `{"command":"ls"}`, client["arguments"])

	// 第一轮上游 body：只有协议载体，没有客户端 tools 声明。
	require.False(t, gjson.GetBytes(bodies[0], "tools").Exists())
	require.Contains(t, string(bodies[0]), "run_officejs")
	protocolText := gjson.GetBytes(bodies[0], "input.0.content.0.text").String()
	require.Contains(t, protocolText, `"name":"exec"`)

	// 第二轮：客户端回传工具调用 + 结果（真实客户端会带完整历史）。
	secondInput, err := json.Marshal([]any{
		openAIBPSMessage("user", "list files"),
		client,
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "a.txt"},
	})
	require.NoError(t, err)
	secondBody := `{"model":"gpt-6-astra","session_id":"conv-tools","input":` + string(secondInput) +
		`,"tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`
	c2, recorder2 := newBPSHandlerTestContext(t, secondBody, nil)
	h.Responses(c2)
	require.Equal(t, http.StatusOK, recorder2.Code)
	var second map[string]any
	require.NoError(t, json.Unmarshal(recorder2.Body.Bytes(), &second))
	require.Equal(t, "gpt-6-astra", second["model"])
	secondItem := bpsTestOutputItem(t, second, 0)
	content, ok := secondItem["content"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, content)
	firstContent, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "done", firstContent["text"])

	// 第二轮上游 body：恢复原始 run_officejs item + 稳定 fc_ 结果。
	require.Len(t, requests, 2)
	replayed := gjson.GetBytes(bodies[1], "input").Array()
	require.Len(t, replayed, 4) // developer protocol, user, native item, output
	require.Equal(t, "function_call", replayed[2].Get("type").String())
	require.Equal(t, "run_officejs", replayed[2].Get("name").String())
	require.Equal(t, "call_1", replayed[2].Get("call_id").String())
	require.Equal(t, "function_call_output", replayed[3].Get("type").String())
	require.Equal(t, "fc_call_1", replayed[3].Get("id").String())
	require.Equal(t, "a.txt", replayed[3].Get("output").String())
}

// SSE 端到端：文本实时输出，原生工具事件在终态转换为客户端 function_call。
func TestOpenAIBPSToolHandlerStreamConversion(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	native := bpsTestNativeCall("call_s", "exec", map[string]any{"command": "pwd"})
	nativeAdded := openAIBPSCloneMap(native)
	nativeAdded["arguments"] = ""
	nativeAdded["status"] = "in_progress"
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.output_item.added", map[string]any{"output_index": 0, "item": nativeAdded}) +
		bpsTestSSEEvent("response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": native["id"], "delta": "officejs-secret"}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 0, "item": native}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(native)})
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{
			selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
			lookupFn: func(context.Context, int64) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
		},
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "text/event-stream", "req_sse", stream), nil
		}),
	})
	body := `{"model":"gpt-6-astra","stream":true,"input":"run pwd","tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Responses(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.function_call_arguments.done")
	require.Contains(t, forwarded, "event: response.completed")
	require.NotContains(t, forwarded, "run_officejs")
	require.NotContains(t, forwarded, "officejs-secret")
}

// 非法工具声明在请求准备阶段返回 400，且不触碰上游。
func TestOpenAIBPSToolHandlerRejectsInvalidTools(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	upstreamCalls := 0
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{
			selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
			lookupFn: func(context.Context, int64) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
		},
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			upstreamCalls++
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsTestCompletedResponse())), nil
		}),
	})
	for _, body := range []string{
		`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search"}]}`,
		`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"remote","parameters":{"$ref":"file:///etc/passwd"}}]}`,
		`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"a"},{"type":"function","name":"a","description":"different"}]}`,
	} {
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorInvalidRequest, code)
	}
	require.Zero(t, upstreamCalls, "非法工具声明不得触碰上游")
}

// 工具 item 过期（Redis 无记录）：409 bps_context_expired，不静默重建历史。
func TestOpenAIBPSToolHandlerExpiredReplay(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	h := NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{
			selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
			lookupFn: func(context.Context, int64) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
		},
		Sessions: store,
		HTTPClient: newBPSUpstreamStub(t, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsTestCompletedResponse())), nil
		}),
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-expired","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"call_gone","output":"x"}],"tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Responses(c)
	require.Equal(t, http.StatusConflict, recorder.Code)
	code, _ := decodeOpenAIBPSAPIError(t, recorder)
	require.Equal(t, OpenAIBPSErrorContextExpired, code)
}
