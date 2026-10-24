package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func bpsTestSSEEvent(typ string, payload map[string]any) string {
	payload["type"] = typ
	return "event: " + typ + "\ndata: " + openAIBPSJSON(payload) + "\n\n"
}

func bpsStreamPrepared(t *testing.T, store *OpenAIBPSSessionStore, sessionKey string, stream bool) *openAIBPSPreparedRequest {
	t.Helper()
	prepared, err := bpsProtocolTestPrepare(t, store, sessionKey, map[string]any{
		"model":  "gpt-6-astra",
		"input":  "task",
		"stream": stream,
		"tools":  []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	return prepared
}

// SSE：文本实时转发；原生工具事件在终态完成解析/校验/落库后才以客户端
// function_call 事件交付，run_officejs 载体与 OfficeJS 代码绝不泄漏。
func TestForwardOpenAIBPSProtocolSSEToolBuffering(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-sse", true)
	native := bpsTestNativeCall("call_sse", "exec", map[string]any{"command": "pwd"})
	nativeAdded := openAIBPSCloneMap(native)
	nativeAdded["status"] = "in_progress"
	nativeAdded["arguments"] = ""

	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1", "status": "in_progress"}}) +
		bpsTestSSEEvent("response.output_item.added", map[string]any{"output_index": 1, "item": nativeAdded}) +
		bpsTestSSEEvent("response.function_call_arguments.delta", map[string]any{"output_index": 1, "item_id": native["id"], "delta": "private-officejs-code"}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 1, "item": native}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(native)})

	c, recorder, writer := newBPSForwardTestContext(t)
	err := forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "req_sse", stream))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.created")
	require.Contains(t, forwarded, "event: response.function_call_arguments.done")
	require.Contains(t, forwarded, "event: response.output_item.done")
	require.Contains(t, forwarded, "event: response.completed")
	require.NotContains(t, forwarded, "response.failed")
	require.NotContains(t, forwarded, "run_officejs")
	require.NotContains(t, forwarded, "private-officejs-code")
	require.Contains(t, forwarded, `"name":"exec"`)
	require.Contains(t, forwarded, `"arguments":"{\"command\":\"pwd\"}"`)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.GreaterOrEqual(t, writer.flushes, 4)

	// 工具 item 已落 Redis，可被下一轮回放。
	saved, err := store.LoadToolItem(context.Background(), 42, "session-sse", "call_sse")
	require.NoError(t, err)
	require.Equal(t, "exec", saved.Name)
}

// 文本先到、工具后交付：工具事件不得早于文本事件。
func TestForwardOpenAIBPSProtocolSSETextBeforeTool(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-order", true)
	native := bpsTestNativeCall("call_order", "exec", map[string]any{"command": "pwd"})
	message := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}}
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{}}}) +
		bpsTestSSEEvent("response.output_text.delta", map[string]any{"output_index": 0, "item_id": "msg_1", "content_index": 0, "delta": "hello"}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 0, "item": message}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(message, native)})
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	forwarded := recorder.Body.String()
	textIndex := strings.Index(forwarded, "event: response.output_text.delta")
	toolIndex := strings.Index(forwarded, "event: response.function_call_arguments.done")
	require.Greater(t, textIndex, -1)
	require.Greater(t, toolIndex, -1)
	require.Less(t, textIndex, toolIndex, "文本必须实时输出，工具事件在终态后交付")
	// 消息条目已经流式输出过（live added + 合成工具 added），终态不得重复消息条目。
	require.Equal(t, 2, strings.Count(forwarded, "event: response.output_item.added"))
}

// 上游断流：补 response.failed(bps_stream_interrupted)，绝不伪造 completed。
func TestForwardOpenAIBPSProtocolSSEInterrupted(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-interrupt", true)
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.output_text.delta", map[string]any{"output_index": 0, "item_id": "msg_1", "delta": "partial"})
	c, recorder, writer := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.failed")
	require.Contains(t, forwarded, openAIBPSStreamInterruptedCode)
	require.NotContains(t, forwarded, "event: response.completed")
	require.GreaterOrEqual(t, writer.flushes, 2)
}

// 上游返回未声明工具：终态校验失败，客户端收到 failed 而不是伪造成功。
func TestForwardOpenAIBPSProtocolUnknownToolFails(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-unknown", true)
	native := bpsTestNativeCall("call_unknown", "missing", map[string]any{})
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(native)})
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.failed")
	require.Contains(t, forwarded, OpenAIBPSErrorUnsupportedTool)
	require.NotContains(t, forwarded, "event: response.completed")
	require.NotContains(t, forwarded, "run_officejs")
}

// 上游未知事件类型：按 bps_unsupported_event 失败。
func TestForwardOpenAIBPSProtocolUnknownEventFails(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-event", true)
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.web_search_call.in_progress", map[string]any{"item_id": "ws1"})
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.failed")
	require.Contains(t, forwarded, OpenAIBPSErrorUnsupportedEvent)
	require.NotContains(t, forwarded, "event: response.completed")
}

// 上游流里出现过工具 item 但终态响应把它丢了：失败，不静默成功。
func TestForwardOpenAIBPSProtocolPendingToolOmitted(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-pending", true)
	native := bpsTestNativeCall("call_pending", "exec", map[string]any{"command": "pwd"})
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 0, "item": native}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse()})
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.failed")
	require.Contains(t, forwarded, OpenAIBPSErrorInvalidToolCall)
	require.NotContains(t, forwarded, "event: response.completed")
}

// 上游返回 unary JSON 但客户端要 SSE：补齐完整事件序列。
func TestForwardOpenAIBPSProtocolJSONUpstreamToStreamClient(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-json-stream", true)
	native := bpsTestNativeCall("call_json", "exec", map[string]any{"command": "pwd"})
	message := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}}
	body := openAIBPSJSON(bpsTestCompletedResponse(message, native))
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "application/json", "", body)))
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.created")
	require.Contains(t, forwarded, "event: response.output_text.delta")
	require.Contains(t, forwarded, "event: response.function_call_arguments.done")
	require.Contains(t, forwarded, "event: response.completed")
	require.NotContains(t, forwarded, "run_officejs")
	require.NotContains(t, forwarded, "response.failed")
}

// JSON 客户端（stream=false）：工具调用转换成 Responses JSON item，不返回 SSE。
func TestForwardOpenAIBPSProtocolJSONClientTool(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-json-client", false)
	native := bpsTestNativeCall("call_js", "exec", map[string]any{"command": "pwd"})
	body := openAIBPSJSON(bpsTestCompletedResponse(native))
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "application/json", "", body)))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, recorder.Body.String(), "event: ")
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	output, ok := response["output"].([]any)
	require.True(t, ok)
	require.Len(t, output, 1)
	client, ok := output[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call", client["type"])
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "fc_call_js", client["id"])
	require.Equal(t, "call_js", client["call_id"])
	require.Equal(t, "gpt-6-astra", response["model"])
	require.NotContains(t, recorder.Body.String(), "run_officejs")
}

// JSON 客户端 + 上游 SSE：读到终态后按 JSON 返回。
func TestForwardOpenAIBPSProtocolSSEUpstreamToJSONClient(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-sse-json", false)
	native := bpsTestNativeCall("call_sj", "exec", map[string]any{"command": "ls"})
	message := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "hi"}}}
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}}) +
		bpsTestSSEEvent("response.output_text.delta", map[string]any{"output_index": 0, "item_id": "msg_1", "delta": "hi"}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 0, "item": message}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(message, native)})
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "event: ")
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	output, ok := response["output"].([]any)
	require.True(t, ok)
	require.Len(t, output, 2)
	require.Equal(t, "message", bpsTestOutputItem(t, response, 0)["type"])
	require.Equal(t, "function_call", bpsTestOutputItem(t, response, 1)["type"])
}

// 上游明确 failed/incomplete：JSON 客户端得到 502，不伪造成功。
func TestForwardOpenAIBPSProtocolFailedTerminal(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-failed", false)
	response := bpsTestCompletedResponse()
	response["status"] = "incomplete"
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(response))))
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	code, _ := decodeOpenAIBPSAPIError(t, recorder)
	require.Equal(t, OpenAIBPSErrorResponseFailed, code)
}

// 上游错误状态码与错误体原样透传（不在网关重新包装）。
func TestForwardOpenAIBPSProtocolUpstreamErrorPassthrough(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-upstream-error", false)
	errorBody := `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusTooManyRequests, "application/json", "req_429", errorBody)))
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.JSONEq(t, errorBody, recorder.Body.String())
	require.Equal(t, "req_429", recorder.Header().Get("x-request-id"))
}

// 并行工具调用：单工具协议下失败。
func TestForwardOpenAIBPSProtocolParallelToolsRejected(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-parallel", false)
	body := openAIBPSJSON(bpsTestCompletedResponse(
		bpsTestNativeCall("call_a", "exec", map[string]any{"command": "pwd"}),
		bpsTestNativeCall("call_b", "exec", map[string]any{"command": "ls"}),
	))
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "application/json", "", body)))
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	code, _ := decodeOpenAIBPSAPIError(t, recorder)
	require.Equal(t, OpenAIBPSErrorParallelTools, code)
}

// SSE 事件的 response 对象不得泄漏上游模型名/内部 output。
func TestForwardOpenAIBPSProtocolHidesUpstreamModel(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared := bpsStreamPrepared(t, store, "session-model", true)
	native := bpsTestNativeCall("call_m", "exec", map[string]any{"command": "pwd"})
	response := bpsTestCompletedResponse(native)
	response["model"] = "gpt-upstream-internal"
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1", "model": "gpt-upstream-internal", "output": []any{}}}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": response})
	c, recorder, _ := newBPSForwardTestContext(t)
	require.NoError(t, forwardOpenAIBPSProtocolResponse(context.Background(), c, store, prepared,
		bpsTestResponse(http.StatusOK, "text/event-stream", "", stream)))
	forwarded := recorder.Body.String()
	require.NotContains(t, forwarded, "gpt-upstream-internal")
	require.Contains(t, forwarded, `"model":"gpt-6-astra"`)
	// 终态 output 里工具已转换；SSE 事件不得包含原生工具名。
	require.NotContains(t, forwarded, "run_officejs")
}
