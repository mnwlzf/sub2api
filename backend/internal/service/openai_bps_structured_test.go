package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func bpsStructuredFormat(strict bool) map[string]any {
	return map[string]any{
		"type": "json_schema", "name": "answer", "strict": strict,
		"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
			"required":   []any{"ok"},
		},
	}
}

func bpsStructuredMessage(id, text string) map[string]any {
	return map[string]any{
		"type": "message", "id": id, "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func bpsStructuredResponse(text string) map[string]any {
	return bpsTestCompletedResponse(bpsStructuredMessage("msg_out", text))
}

func bpsStructuredBody(format map[string]any, stream bool) []byte {
	return []byte(openAIBPSJSON(map[string]any{
		"model":      "gpt-6-astra",
		"session_id": "conv-structured",
		"input":      "Answer the request",
		"stream":     stream,
		"text":       map[string]any{"format": format},
	}))
}

// bpsSSEData 返回指定事件类型的 data JSON 文本（第一个匹配）。
func bpsSSEData(t *testing.T, body, eventType string) string {
	t.Helper()
	for _, block := range strings.Split(body, "\n\n") {
		var typ, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if typ == eventType && data != "" {
			return data
		}
	}
	return ""
}

// 结构化输出：JSON 与 SSE 同一套终态校验；未校验文本绝不提前发送；成功时
// 返回客户端保留声明。
func TestOpenAIBPSStructuredJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			for _, valid := range []bool{false, true} {
				name := openAIBPSJSON([]bool{stream, strict, valid})
				t.Run(name, func(t *testing.T) {
					enableOpenAIBPSForTest(t)
					store, _ := newOpenAIBPSTestStore(t)
					text := `{"ok":true}`
					if !valid {
						text = `{"ok":"wrong"}`
					}
					final := bpsStructuredResponse(text)
					upstreamBody := openAIBPSJSON(final)
					contentType := "application/json"
					if stream {
						contentType = "text/event-stream"
						upstreamBody = bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{
							"id": "resp_s", "status": "in_progress", "output": []any{}, "output_text": "unchecked-created",
						}}) +
							bpsTestSSEEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{
								"type": "message", "id": "msg_stream", "role": "assistant", "content": []any{},
							}}) +
							bpsTestSSEEvent("response.output_text.delta", map[string]any{
								"item_id": "msg_stream", "output_index": 0, "content_index": 0, "delta": "unchecked-delta",
							}) +
							bpsTestSSEEvent("response.output_item.done", map[string]any{
								"output_index": 0, "item": bpsStructuredMessage("msg_stream", "unchecked-done"),
							}) +
							bpsTestSSEEvent("response.completed", map[string]any{"response": final})
					}
					var captured []byte
					h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
						captured, _ = io.ReadAll(req.Body)
						return bpsTestResponse(http.StatusOK, contentType, "req_structured", upstreamBody), nil
					})
					format := bpsStructuredFormat(strict)
					c, recorder := newBPSHandlerTestContext(t, string(bpsStructuredBody(format, stream)), nil)
					h.Responses(c)

					// 上游 body：没有原生 text.format，只有 developer 提示词约束。
					require.False(t, gjson.GetBytes(captured, "text").Exists())
					require.Contains(t, string(captured), "structured final answer")
					require.Contains(t, string(captured), "json_schema")

					usage, hasUsage := c.Get(OpenAIBPSUsageContextKey)
					require.True(t, hasUsage)
					require.Equal(t, int64(10), gjson.Get(openAIBPSJSON(usage), "input_tokens").Int())
					if valid {
						require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
						if stream {
							forwarded := recorder.Body.String()
							require.NotContains(t, forwarded, "unchecked-")
							require.Contains(t, forwarded, "event: response.output_text.delta")
							require.Contains(t, forwarded, "event: response.completed")
							created := bpsSSEData(t, forwarded, "response.created")
							require.NotEmpty(t, created)
							require.Equal(t, "json_schema", gjson.Get(created, "response.text.format.type").String())
							completed := bpsSSEData(t, forwarded, "response.completed")
							require.Equal(t, "json_schema", gjson.Get(completed, "response.text.format.type").String())
							require.Equal(t, text, gjson.Get(completed, "response.output.0.content.0.text").String())
						} else {
							require.Equal(t, "json_schema", gjson.Get(recorder.Body.String(), "text.format.type").String())
							require.Equal(t, text, gjson.Get(recorder.Body.String(), "output.0.content.0.text").String())
						}
						return
					}
					require.NotContains(t, recorder.Body.String(), "unchecked-")
					if stream {
						require.Contains(t, recorder.Body.String(), "event: response.failed")
						require.NotContains(t, recorder.Body.String(), "event: response.completed")
						require.Contains(t, recorder.Body.String(), OpenAIBPSErrorInvalidStructuredOutput)
					} else {
						require.Equal(t, http.StatusBadGateway, recorder.Code)
						code, _ := decodeOpenAIBPSAPIError(t, recorder)
						require.Equal(t, OpenAIBPSErrorInvalidStructuredOutput, code)
					}
				})
			}
		}
	}
}

// 本地终态校验边界：json_object 必须是对象；json_schema 即使 strict=false
// 也校验；大整数不丢精度；只允许文档内引用；文本上限 16 MiB。
func TestOpenAIBPSStructuredValidationBoundaries(t *testing.T) {
	object, err := openAIBPSPrepareStructuredOutput(map[string]any{"format": map[string]any{"type": "json_object"}})
	require.NoError(t, err)
	for _, text := range []string{`[]`, `null`, `true`, `{"ok":true} trailing`, `{"ok":true}{}`, "```json\n{}\n```", ""} {
		require.Error(t, object.validate(bpsStructuredResponse(text)), text)
	}
	require.NoError(t, object.validate(bpsStructuredResponse(`{"ok":true}`)))
	require.Error(t, object.validate(bpsStructuredResponse(strings.Repeat(" ", openAIBPSMaxStructuredTextBytes+1))))

	var format map[string]any
	require.NoError(t, openAIBPSDecode([]byte(`{"type":"json_schema","name":"large","schema":{"type":"object","properties":{"id":{"const":9007199254740993}},"required":["id"]}}`), &format))
	large, err := openAIBPSPrepareStructuredOutput(map[string]any{"format": format})
	require.NoError(t, err)
	require.NoError(t, large.validate(bpsStructuredResponse(`{"id":9007199254740993}`)))
	require.Error(t, large.validate(bpsStructuredResponse(`{"id":9007199254740992}`)))
	schema, ok := format["schema"].(map[string]any)
	require.True(t, ok)
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	idProperty, ok := properties["id"].(map[string]any)
	require.True(t, ok)
	require.IsType(t, json.Number(""), idProperty["const"])

	// 只允许文档内引用。
	require.NoError(t, openAIBPSDecode([]byte(`{"type":"json_schema","name":"local","schema":{"type":"object","$defs":{"flag":{"type":"boolean"}},"properties":{"ok":{"$ref":"#/$defs/flag"}},"required":["ok"]}}`), &format))
	local, err := openAIBPSPrepareStructuredOutput(map[string]any{"format": format})
	require.NoError(t, err)
	require.NoError(t, local.validate(bpsStructuredResponse(`{"ok":true}`)))
	for _, schema := range []map[string]any{
		{"$ref": "file:///etc/passwd"},
		{"$ref": "https://example.invalid/schema.json"},
		{"type": "invalid"},
		{"type": "object", "properties": "not-a-schema-map"},
		{"description": strings.Repeat("x", 1<<20)},
	} {
		_, err := openAIBPSPrepareStructuredOutput(map[string]any{"format": map[string]any{"type": "json_schema", "name": "bad", "schema": schema}})
		require.Error(t, err, openAIBPSJSON(schema))
	}
	// strict=false 也校验。
	loose, err := openAIBPSPrepareStructuredOutput(map[string]any{"format": bpsStructuredFormat(false)})
	require.NoError(t, err)
	require.Error(t, loose.validate(bpsStructuredResponse(`{"ok":"wrong"}`)))
}

// 非法声明在请求阶段 400 暴露，绝不触碰上游。
func TestOpenAIBPSStructuredRejectsInvalidRequests(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	upstreamCalls := 0
	h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		upstreamCalls++
		return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsStructuredResponse(`{"ok":true}`))), nil
	})
	cases := map[string]any{
		"format not object": true,
		"unknown type":      map[string]any{"type": "unknown"},
		"bad name":          map[string]any{"type": "json_schema", "name": "bad name", "schema": map[string]any{}},
		"strict not bool":   map[string]any{"type": "json_schema", "name": "ok", "strict": "yes", "schema": map[string]any{}},
		"missing schema":    map[string]any{"type": "json_schema", "name": "ok"},
		"external file ref": map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"$ref": "file:///etc/passwd"}},
		"external url ref":  map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"$ref": "https://example.invalid/schema.json"}},
		"invalid type name": map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"type": "invalid"}},
		"bad properties":    map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"type": "object", "properties": "nope"}},
		"bad required":      map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"type": "object", "required": "ok"}},
		"bad additional":    map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"type": "object", "additionalProperties": []any{}}},
		"oversized schema":  map[string]any{"type": "json_schema", "name": "ok", "schema": map[string]any{"type": "object", "description": strings.Repeat("x", 1<<20)}},
		"unsupported field": map[string]any{"type": "json_object", "schema": map[string]any{}},
	}
	for name, format := range cases {
		t.Run(name, func(t *testing.T) {
			body := openAIBPSJSON(map[string]any{"model": "gpt-6-astra", "input": "hi", "text": map[string]any{"format": format}})
			c, recorder := newBPSHandlerTestContext(t, body, nil)
			h.Responses(c)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			code, _ := decodeOpenAIBPSAPIError(t, recorder)
			require.Equal(t, OpenAIBPSErrorInvalidRequest, code)
		})
	}
	// text 本身不是对象也按 400 处理。
	c, recorder := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":"hi","text":"json"}`, nil)
	h.Responses(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, upstreamCalls, "非法结构化声明不得触碰上游")

	// text 类型是普通文本：不做结构化校验，也不带原生 text 给上游。
	c2, recorder2 := newBPSHandlerTestContext(t, `{"model":"gpt-6-astra","input":"hi","text":{"format":{"type":"text"}}}`, nil)
	h.Responses(c2)
	require.Equal(t, http.StatusOK, recorder2.Code, recorder2.Body.String())
	require.Equal(t, 1, upstreamCalls)
	require.Equal(t, `{"ok":true}`, gjson.Get(recorder2.Body.String(), "output.0.content.0.text").String())
}

// 工具调用与明确拒答是独立协议项：不因结构化校验失败。
func TestOpenAIBPSStructuredToolsAndRefusals(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	native := bpsTestNativeCall("call_s", "exec", map[string]any{"command": "pwd"})
	call := 0
	h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		call++
		switch call {
		case 1:
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsTestCompletedResponse(native))), nil
		case 2:
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsStructuredResponse(`{"ok":true}`))), nil
		default:
			refusal := map[string]any{"type": "message", "id": "msg_refusal", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "refusal", "refusal": "Cannot provide that"}}}
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsTestCompletedResponse(refusal))), nil
		}
	})
	body := openAIBPSJSON(map[string]any{
		"model": "gpt-6-astra", "session_id": "conv-structured-tools", "input": "inspect then return JSON",
		"text":  map[string]any{"format": bpsStructuredFormat(true)},
		"tools": []any{bpsTestFunctionTool("exec")},
	})
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Responses(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "function_call")
	require.NotContains(t, recorder.Body.String(), OpenAIBPSErrorInvalidStructuredOutput)
	require.NotContains(t, recorder.Body.String(), "run_officejs")

	// 回传工具结果后继续：最终 JSON 通过校验。
	var first map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &first))
	clientCall := bpsTestOutputItem(t, first, 0)
	secondInput := []any{
		openAIBPSMessage("user", "inspect then return JSON"),
		clientCall,
		map[string]any{"type": "function_call_output", "call_id": "call_s", "output": "/workspace"},
	}
	secondBody := openAIBPSJSON(map[string]any{
		"model": "gpt-6-astra", "session_id": "conv-structured-tools", "input": secondInput,
		"text": map[string]any{"format": bpsStructuredFormat(true)},
	})
	c2, recorder2 := newBPSHandlerTestContext(t, secondBody, nil)
	h.Responses(c2)
	require.Equal(t, http.StatusOK, recorder2.Code, recorder2.Body.String())
	require.Equal(t, `{"ok":true}`, gjson.Get(recorder2.Body.String(), "output.0.content.0.text").String())

	// 纯拒答按独立协议项交付，不要求 JSON。
	c3, recorder3 := newBPSHandlerTestContext(t, string(bpsStructuredBody(bpsStructuredFormat(true), false)), nil)
	h.Responses(c3)
	require.Equal(t, http.StatusOK, recorder3.Code, recorder3.Body.String())
	require.Contains(t, recorder3.Body.String(), "Cannot provide that")
	require.NotContains(t, recorder3.Body.String(), OpenAIBPSErrorInvalidStructuredOutput)

	// SSE 拒答：终态校验通过后以 refusal 事件交付。
	refusalStream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_r", "status": "in_progress"}}) +
		bpsTestSSEEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_refusal", "content": []any{}}}) +
		bpsTestSSEEvent("response.refusal.delta", map[string]any{"item_id": "msg_refusal", "output_index": 0, "content_index": 0, "delta": "Cannot provide that"}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_refusal", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "refusal", "refusal": "Cannot provide that"}}}}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(map[string]any{"type": "message", "id": "msg_refusal", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "refusal", "refusal": "Cannot provide that"}}})})
	h4 := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		return bpsTestResponse(http.StatusOK, "text/event-stream", "", refusalStream), nil
	})
	c4, recorder4 := newBPSHandlerTestContext(t, string(bpsStructuredBody(bpsStructuredFormat(true), true)), nil)
	h4.Responses(c4)
	require.Equal(t, http.StatusOK, recorder4.Code, recorder4.Body.String())
	require.Contains(t, recorder4.Body.String(), "event: response.refusal.delta")
	require.Contains(t, recorder4.Body.String(), "event: response.refusal.done")
	require.NotContains(t, recorder4.Body.String(), "event: response.failed")
}

// 结构化流式：未校验文本绝不泄漏；上游失败/断流时补 response.failed 并保留
// 已取得的 usage。
func TestOpenAIBPSStructuredInterruptedStream(t *testing.T) {
	enableOpenAIBPSForTest(t)
	body := string(bpsStructuredBody(bpsStructuredFormat(true), true))

	t.Run("interrupted", func(t *testing.T) {
		enableOpenAIBPSForTest(t)
		store, _ := newOpenAIBPSTestStore(t)
		stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_i", "status": "in_progress"}}) +
			bpsTestSSEEvent("response.output_text.delta", map[string]any{"delta": "unvalidated partial content"})
		h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "text/event-stream", "", stream), nil
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		require.NotContains(t, recorder.Body.String(), "unvalidated partial content")
		require.NotContains(t, recorder.Body.String(), "event: response.completed")
		require.Contains(t, recorder.Body.String(), "event: response.failed")
	})

	for _, terminal := range []string{"response.failed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			enableOpenAIBPSForTest(t)
			store, _ := newOpenAIBPSTestStore(t)
			stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_f", "status": "in_progress"}}) +
				bpsTestSSEEvent("response.output_text.delta", map[string]any{"delta": "unvalidated partial content"}) +
				bpsTestSSEEvent(terminal, map[string]any{"response": map[string]any{"status": "failed", "usage": map[string]any{"input_tokens": 42}}})
			h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
				return bpsTestResponse(http.StatusOK, "text/event-stream", "", stream), nil
			})
			c, recorder := newBPSHandlerTestContext(t, body, nil)
			h.Responses(c)
			require.NotContains(t, recorder.Body.String(), "unvalidated partial content")
			require.NotContains(t, recorder.Body.String(), "event: response.completed")
			require.Contains(t, recorder.Body.String(), "event: response.failed")
			usage, ok := c.Get(OpenAIBPSUsageContextKey)
			require.True(t, ok)
			require.Equal(t, int64(42), gjson.Get(openAIBPSJSON(usage), "input_tokens").Int())
		})
	}
}

// 压缩请求即使声明了 text.format 也必须返回真实文本摘要（结构化只作用于
// 普通 Responses）。
func TestOpenAIBPSStructuredCompactionStaysText(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	var captured []byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		captured, _ = io.ReadAll(req.Body)
		return bpsCompactSummaryResponse("plain summary text"), nil
	})
	body := openAIBPSJSON(map[string]any{
		"model": "gpt-6-astra", "session_id": "conv-structured-compact", "input": "original task",
		"text": map[string]any{"format": bpsStructuredFormat(true)},
	})
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	item := bpsCompactOutput(t, recorder.Body.Bytes())
	require.Equal(t, "compaction", item["type"])
	require.False(t, gjson.GetBytes(captured, "text").Exists())
	require.Contains(t, string(captured), "Summarize the conversation")
	ref, ok := item["encrypted_content"].(string)
	require.True(t, ok)
	state, err := store.LoadCompactState(context.Background(), 42, mustBPSSessionKey(t, 42, body), ref)
	require.NoError(t, err)
	require.Equal(t, "plain summary text", state.Summary)
}

// 结构化请求的普通文本仍实时转发（只有最终答案被缓冲校验）。
func TestOpenAIBPSStructuredReasoningStillStreams(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_reason", "status": "in_progress"}}) +
		bpsTestSSEEvent("response.reasoning_summary_text.delta", map[string]any{"item_id": "rs_1", "output_index": 0, "delta": "thinking"}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsStructuredResponse(`{"ok":true}`)})
	h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		return bpsTestResponse(http.StatusOK, "text/event-stream", "", stream), nil
	})
	c, recorder := newBPSHandlerTestContext(t, string(bpsStructuredBody(bpsStructuredFormat(true), true)), nil)
	h.Responses(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "response.reasoning_summary_text.delta")
	require.Contains(t, recorder.Body.String(), "event: response.completed")
	require.NotContains(t, recorder.Body.String(), "event: response.failed")
}

// json_object 的 HTTP 路径：对象通过，数组/标量按 502 拒绝。
func TestOpenAIBPSStructuredJSONObject(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{`{"ok":true}`, true},
		{`[]`, false},
		{`"text"`, false},
		{`{"ok":true} trailing`, false},
	} {
		enableOpenAIBPSForTest(t)
		store, _ := newOpenAIBPSTestStore(t)
		h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsStructuredResponse(tc.text))), nil
		})
		body := openAIBPSJSON(map[string]any{
			"model": "gpt-6-astra", "session_id": "conv-json-object", "input": "return json",
			"text": map[string]any{"format": map[string]any{"type": "json_object"}},
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Responses(c)
		if tc.ok {
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, tc.text, gjson.Get(recorder.Body.String(), "output.0.content.0.text").String())
			require.Equal(t, "json_object", gjson.Get(recorder.Body.String(), "text.format.type").String())
			continue
		}
		require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorInvalidStructuredOutput, code)
	}
}
