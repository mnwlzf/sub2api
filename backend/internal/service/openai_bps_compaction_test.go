package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// bpsCompactTestHandler 构造一个可打桩上游的 BPS handler（会话绑定/账号查找
// 都指向同一个测试账号）。
func bpsCompactTestHandler(t *testing.T, store *OpenAIBPSSessionStore, upstream func(*http.Request) (*http.Response, error)) *OpenAIBPSHandler {
	t.Helper()
	return NewOpenAIBPSHandler(OpenAIBPSHandlerDeps{
		Resolver: &bpsTestResolver{
			selectFn: func(context.Context, *gin.Context, string) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
			lookupFn: func(context.Context, int64) (*Account, error) {
				return bpsTestAccount(7, "token-7", "ws-7"), nil
			},
		},
		Sessions:   store,
		HTTPClient: newBPSUpstreamStub(t, upstream),
	})
}

// bpsCompactSummaryResponse 返回一个带真实文本摘要的完成响应。
func bpsCompactSummaryResponse(summary string) *http.Response {
	message := map[string]any{
		"type": "message", "id": "msg_summary", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": summary}},
	}
	return bpsTestResponse(http.StatusOK, "application/json", "req_compact",
		openAIBPSJSON(bpsTestCompletedResponse(message)))
}

func bpsCompactRef(t *testing.T, recorderBody []byte) string {
	t.Helper()
	ref, ok := bpsCompactOutput(t, recorderBody)["encrypted_content"].(string)
	require.True(t, ok, "compaction output must carry an encrypted_content reference")
	return ref
}

func bpsCompactOutput(t *testing.T, recorderBody []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorderBody, &payload), "body: %s", string(recorderBody))
	output, ok := payload["output"].([]any)
	require.True(t, ok, "body: %s", string(recorderBody))
	require.Len(t, output, 1)
	item, ok := output[0].(map[string]any)
	require.True(t, ok)
	return item
}

// /responses/compact：发起真实摘要请求（关闭客户端工具）、摘要落 Redis、
// 客户端只收到版本化引用。
func TestOpenAIBPSCompactEndpointStoresSummaryAndHidesReference(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, server := newOpenAIBPSTestStore(t)
	var bodies [][]byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodies = append(bodies, body)
		return bpsCompactSummaryResponse("Real summary containing pending work."), nil
	})

	body := `{"model":"gpt-6-astra","session_id":"conv-compact","input":"original task","tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	item := bpsCompactOutput(t, recorder.Body.Bytes())
	require.Equal(t, "compaction", item["type"])
	require.Equal(t, "completed", item["status"])
	ref, ok := item["encrypted_content"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(ref, openAIBPSCompactPrefix), ref)
	require.Equal(t, "response.compaction", gjson.Get(recorder.Body.String(), "object").String())
	require.Equal(t, "gpt-6-astra", gjson.Get(recorder.Body.String(), "model").String())
	// 客户端只收到不透明引用：真实摘要文本不能出现在响应里。
	require.NotContains(t, recorder.Body.String(), "Real summary containing pending work.")

	require.Len(t, bodies, 1)
	upstream := bodies[0]
	// 压缩请求关闭客户端工具：不收集 tools、不注入工具目录。
	require.False(t, gjson.GetBytes(upstream, "tools").Exists())
	require.NotContains(t, string(upstream), "run_officejs")
	require.NotContains(t, string(upstream), `"exec"`)
	// 真实摘要请求：追加摘要指令；原生 text.format 不发给上游。
	require.Contains(t, string(upstream), "Summarize the conversation so a successor assistant")
	require.False(t, gjson.GetBytes(upstream, "text").Exists())
	// 引用字符串绝不进入上游 body。
	require.NotContains(t, string(upstream), openAIBPSCompactPrefix)
	require.NotContains(t, string(upstream), ref)
	turnID := gjson.GetBytes(upstream, "metadata.turn_id").String()
	require.NotEmpty(t, turnID)
	require.Equal(t, "1", gjson.GetBytes(upstream, "metadata.agent_iteration").String())

	// 摘要与引用索引都落 Redis，7 天闲置过期。
	sessionKey, explicit := OpenAIBPSSessionKey(42, []byte(body))
	require.True(t, explicit)
	state, err := store.LoadCompactState(context.Background(), 42, sessionKey, ref)
	require.NoError(t, err)
	require.Equal(t, "Real summary containing pending work.", state.Summary)
	require.Equal(t, turnID, state.TurnID)
	require.Equal(t, 1, state.AgentIteration)
	require.Greater(t, server.TTL(OpenAIBPSCompactRedisKey(42, sessionKey, ref)), 6*24*time.Hour)
	reference, err := store.LoadCompactReference(context.Background(), 42, ref)
	require.NoError(t, err)
	require.Equal(t, sessionKey, reference.SessionKey)
	require.Greater(t, server.TTL(OpenAIBPSCompactReferenceRedisKey(42, ref)), 6*24*time.Hour)
}

// 压缩后的后续请求：服务端解析引用恢复摘要，引用不外发；没有显式会话 ID 时
// 由引用恢复原会话；换一个服务实例（共享 Redis）仍能恢复。
func TestOpenAIBPSCompactResumeAcrossInstances(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, server := newOpenAIBPSTestStore(t)
	var compactBodies [][]byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		compactBodies = append(compactBodies, body)
		return bpsCompactSummaryResponse("Summary with the original task and pending step."), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-resume","input":"original task"}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	ref := bpsCompactRef(t, recorder.Body.Bytes())
	firstTurn := gjson.GetBytes(compactBodies[0], "metadata.turn_id").String()

	// 新实例（同一 Redis）继续：客户端只回传压缩 item（没有新的用户消息）。
	var resumedBodies [][]byte
	freshStore := NewOpenAIBPSSessionStore(newOpenAIBPSTestClient(t, server.Addr()))
	fresh := bpsCompactTestHandler(t, freshStore, func(req *http.Request) (*http.Response, error) {
		next, _ := io.ReadAll(req.Body)
		resumedBodies = append(resumedBodies, next)
		message := map[string]any{"type": "message", "id": "msg_after", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "continued"}}}
		return bpsTestResponse(http.StatusOK, "application/json", "req_resume", openAIBPSJSON(bpsTestCompletedResponse(message))), nil
	})
	nextBody := `{"model":"gpt-6-astra","input":[{"type":"compaction","encrypted_content":"` + ref + `"}],"tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}`
	next, nextRecorder := newBPSHandlerTestContext(t, nextBody, nil)
	fresh.Responses(next)

	require.Equal(t, http.StatusOK, nextRecorder.Code, nextRecorder.Body.String())
	require.Len(t, resumedBodies, 1)
	resumed := string(resumedBodies[0])
	require.Contains(t, resumed, "Summary with the original task and pending step.")
	require.Contains(t, resumed, "conversation_summary")
	require.NotContains(t, resumed, openAIBPSCompactPrefix)
	require.NotContains(t, resumed, ref)
	// 没有新的用户消息：保持原轮次，迭代基数不变。
	require.Equal(t, firstTurn, gjson.GetBytes(resumedBodies[0], "metadata.turn_id").String())
	require.Equal(t, "1", gjson.GetBytes(resumedBodies[0], "metadata.agent_iteration").String())
	// 会话恢复后仍然走绑定账号（按 ID 精确取号）。
	resolver, ok := fresh.deps.Resolver.(*bpsTestResolver)
	require.True(t, ok)
	_, lookupCalls, _ := resolver.counts()
	require.Equal(t, 1, lookupCalls)
	require.Equal(t, "continued", gjson.Get(nextRecorder.Body.String(), "output.0.content.0.text").String())
}

// 恢复后仅新增工具结果：轮次不变，agent_iteration 递增；重试同一请求不改
// 上游 body。
func TestOpenAIBPSCompactTurnAndIteration(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	var bodies [][]byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodies = append(bodies, body)
		return bpsCompactSummaryResponse("summary"), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-iter","input":"original task"}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	ref := bpsCompactRef(t, recorder.Body.Bytes())
	firstTurn := gjson.GetBytes(bodies[0], "metadata.turn_id").String()
	sessionKey, _ := OpenAIBPSSessionKey(42, []byte(body))

	// 工具调用结果已保存在 Redis（真实多轮里由上一轮响应写入）。
	native := bpsTestNativeCall("call_1", "exec", map[string]any{"command": "pwd"})
	require.NoError(t, store.SaveToolItem(context.Background(), 42, sessionKey, &openAIBPSToolItem{
		CallID: "call_1", ItemID: stringValue(native["id"]), Name: "exec", Kind: "function", Native: native,
	}))

	upstreamBodies := bodies
	h2 := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		next, _ := io.ReadAll(req.Body)
		upstreamBodies = append(upstreamBodies, next)
		message := map[string]any{"type": "message", "id": "msg_x", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "done"}}}
		return bpsTestResponse(http.StatusOK, "application/json", "req_next", openAIBPSJSON(bpsTestCompletedResponse(message))), nil
	})
	nextBody := `{"model":"gpt-6-astra","session_id":"conv-iter","input":[{"type":"compaction","encrypted_content":"` + ref + `"},{"type":"function_call_output","call_id":"call_1","output":"/workspace"}]}`
	next, nextRecorder := newBPSHandlerTestContext(t, nextBody, nil)
	h2.Responses(next)
	require.Equal(t, http.StatusOK, nextRecorder.Code, nextRecorder.Body.String())

	require.Equal(t, firstTurn, gjson.GetBytes(upstreamBodies[1], "metadata.turn_id").String())
	require.Equal(t, "2", gjson.GetBytes(upstreamBodies[1], "metadata.agent_iteration").String())
	// 引用仍然不外发。
	require.NotContains(t, string(upstreamBodies[1]), openAIBPSCompactPrefix)

	// 重试同一请求：上游 body 完全一致（纯函数派生，不推进任何计数器）。
	preparedAgain, err := prepareOpenAIBPSRequest(context.Background(), store, bpsTestAccount(7, "token-7", "ws-7"), 42, sessionKey, []byte(nextBody))
	require.NoError(t, err)
	require.JSONEq(t, string(upstreamBodies[1]), string(preparedAgain.Body))
}

// 连续压缩：第二次压缩没有新的用户消息，保留第一次记录的轮次锚点。
func TestOpenAIBPSCompactionKeepsOriginalTurnAnchor(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	responses := []string{"first summary", "second summary"}
	call := 0
	var bodies [][]byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodies = append(bodies, body)
		if call >= len(responses) {
			// 第三次是普通 Responses（恢复压缩后继续），返回普通文本。
			message := map[string]any{"type": "message", "id": "msg_after_chain", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "continued"}}}
			return bpsTestResponse(http.StatusOK, "application/json", "req_after_chain", openAIBPSJSON(bpsTestCompletedResponse(message))), nil
		}
		summary := responses[call]
		call++
		return bpsCompactSummaryResponse(summary), nil
	})
	firstBody := `{"model":"gpt-6-astra","session_id":"conv-chain","input":"original task"}`
	sessionKey := mustBPSSessionKey(t, 42, firstBody)
	c, recorder := newBPSHandlerTestContext(t, firstBody, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	firstRef := bpsCompactRef(t, recorder.Body.Bytes())
	firstTurn := gjson.GetBytes(bodies[0], "metadata.turn_id").String()
	firstState, err := store.LoadCompactState(context.Background(), 42, mustBPSSessionKey(t, 42, firstBody), firstRef)
	require.NoError(t, err)
	require.Equal(t, firstTurn, firstState.TurnID)

	// 第二次压缩：只回传第一次的引用，没有新的用户消息。
	secondBody := `{"model":"gpt-6-astra","session_id":"conv-chain","input":[{"type":"compaction","encrypted_content":"` + firstRef + `"}]}`
	c2, recorder2 := newBPSHandlerTestContext(t, secondBody, nil)
	h.Compact(c2)
	require.Equal(t, http.StatusOK, recorder2.Code, recorder2.Body.String())
	secondRef := bpsCompactRef(t, recorder2.Body.Bytes())
	require.NotEqual(t, firstRef, secondRef)
	require.Equal(t, firstTurn, gjson.GetBytes(bodies[1], "metadata.turn_id").String())
	require.Contains(t, string(bodies[1]), "first summary")
	require.NotContains(t, string(bodies[1]), openAIBPSCompactPrefix)

	secondState, err := store.LoadCompactState(context.Background(), 42, sessionKey, secondRef)
	require.NoError(t, err)
	require.Equal(t, "second summary", secondState.Summary)
	// 最初轮次锚点被保留。
	require.Equal(t, firstState.TurnID, secondState.TurnID)
	require.Equal(t, firstState.AgentIteration, secondState.AgentIteration)

	// 恢复第二次压缩后仍然是最初轮次。
	thirdBody := `{"model":"gpt-6-astra","session_id":"conv-chain","input":[{"type":"compaction","encrypted_content":"` + secondRef + `"}]}`
	c3, recorder3 := newBPSHandlerTestContext(t, thirdBody, nil)
	h.Responses(c3)
	require.Equal(t, http.StatusOK, recorder3.Code, recorder3.Body.String())
	require.Equal(t, firstTurn, gjson.GetBytes(bodies[2], "metadata.turn_id").String())
}

func mustBPSSessionKey(t *testing.T, apiKeyID int64, body string) string {
	t.Helper()
	key, _ := OpenAIBPSSessionKey(apiKeyID, []byte(body))
	require.NotEmpty(t, key)
	return key
}

// 旧版本压缩记录（只有 summary 字段或只有 JSON 字符串）仍可读取。
func TestOpenAIBPSCompactLegacyRecordsReadable(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, server := newOpenAIBPSTestStore(t)
	var bodies [][]byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		bodies = append(bodies, body)
		message := map[string]any{"type": "message", "id": "msg_ok", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}
		return bpsTestResponse(http.StatusOK, "application/json", "req_ok", openAIBPSJSON(bpsTestCompletedResponse(message))), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-legacy","input":"original task"}`
	sessionKey := mustBPSSessionKey(t, 42, body)

	for name, record := range map[string]string{
		"object without turn fields": `{"summary":"legacy summary text"}`,
		"plain json string":          `"legacy string summary"`,
	} {
		t.Run(name, func(t *testing.T) {
			ref := openAIBPSCompactPrefix + "legacy_" + strings.ReplaceAll(name, " ", "_")
			_ = server.Set(OpenAIBPSCompactRedisKey(42, sessionKey, ref), record)
			_ = server.Set(OpenAIBPSCompactReferenceRedisKey(42, ref), `{"session_key":"`+sessionKey+`"}`)
			request := `{"model":"gpt-6-astra","session_id":"conv-legacy","input":[{"type":"compaction","encrypted_content":"` + ref + `"}]}`
			c, recorder := newBPSHandlerTestContext(t, request, nil)
			h.Responses(c)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			upstream := string(bodies[len(bodies)-1])
			require.Contains(t, upstream, "legacy")
			require.NotContains(t, upstream, openAIBPSCompactPrefix)
			// 旧记录没有 turn_id：按 scope+ref 派生稳定值。
			require.NotEmpty(t, gjson.GetBytes(bodies[len(bodies)-1], "metadata.turn_id").String())
			require.Equal(t, "1", gjson.GetBytes(bodies[len(bodies)-1], "metadata.agent_iteration").String())
		})
	}
}

// 压缩失败：返回错误、不替换客户端历史、不写任何压缩状态；异常断流不伪造成功。
func TestOpenAIBPSCompactFailuresDoNotStoreState(t *testing.T) {
	enableOpenAIBPSForTest(t)
	body := `{"model":"gpt-6-astra","session_id":"conv-fail","input":"original task"}`

	t.Run("upstream 5xx", func(t *testing.T) {
		store, server := newOpenAIBPSTestStore(t)
		h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusServiceUnavailable, "application/json", "", `{"error":{"message":"busy"}}`), nil
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Compact(c)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.NotContains(t, recorder.Body.String(), "compaction")
		require.Empty(t, bpsCompactKeys(server))
	})

	t.Run("tool instead of summary", func(t *testing.T) {
		store, server := newOpenAIBPSTestStore(t)
		native := bpsTestNativeCall("call_x", "exec", map[string]any{"command": "pwd"})
		h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(bpsTestCompletedResponse(native))), nil
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Compact(c)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorCompactionFailed, code)
		require.Empty(t, bpsCompactKeys(server))
	})

	t.Run("empty summary", func(t *testing.T) {
		store, server := newOpenAIBPSTestStore(t)
		h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
			empty := bpsTestCompletedResponse()
			empty["output"] = []any{}
			return bpsTestResponse(http.StatusOK, "application/json", "", openAIBPSJSON(empty)), nil
		})
		c, recorder := newBPSHandlerTestContext(t, body, nil)
		h.Compact(c)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, OpenAIBPSErrorCompactionFailed, code)
		require.Empty(t, bpsCompactKeys(server))
	})

	t.Run("interrupted stream", func(t *testing.T) {
		store, server := newOpenAIBPSTestStore(t)
		interrupted := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1", "status": "in_progress"}}) +
			bpsTestSSEEvent("response.output_text.delta", map[string]any{"delta": "partial summary"})
		h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
			return bpsTestResponse(http.StatusOK, "text/event-stream", "", interrupted), nil
		})
		streamBody := `{"model":"gpt-6-astra","session_id":"conv-fail","input":"original task","stream":true}`
		c, recorder := newBPSHandlerTestContext(t, streamBody, nil)
		h.Compact(c)
		require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		code, _ := decodeOpenAIBPSAPIError(t, recorder)
		require.Equal(t, openAIBPSStreamInterruptedCode, code)
		require.NotContains(t, recorder.Body.String(), "event: response.completed")
		require.NotContains(t, recorder.Body.String(), "partial summary")
		require.Empty(t, bpsCompactKeys(server))
	})
}

func bpsCompactKeys(server interface{ Keys() []string }) []string {
	keys := make([]string, 0)
	for _, key := range server.Keys() {
		if strings.HasPrefix(key, "bps:compact") {
			keys = append(keys, key)
		}
	}
	return keys
}

// 普通 Responses 里的 compaction_trigger 也触发真实摘要请求（响应仍是 response）。
func TestOpenAIBPSCompactionTriggerOnResponses(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	var bodies [][]byte
	h := bpsCompactTestHandler(t, store, func(req *http.Request) (*http.Response, error) {
		upstream, _ := io.ReadAll(req.Body)
		bodies = append(bodies, upstream)
		return bpsCompactSummaryResponse("trigger summary"), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-trigger","input":[{"role":"user","content":"task"},{"type":"compaction_trigger"}],"tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Responses(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	item := bpsCompactOutput(t, recorder.Body.Bytes())
	require.Equal(t, "compaction", item["type"])
	refText, ok := item["encrypted_content"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(refText, openAIBPSCompactPrefix))
	// 普通入口保持 object=response。
	require.Equal(t, "response", gjson.Get(recorder.Body.String(), "object").String())
	require.Contains(t, string(bodies[0]), "Summarize the conversation")
	require.NotContains(t, string(bodies[0]), "compaction_trigger")
	require.NotContains(t, string(bodies[0]), "run_officejs")
}

// 引用按 API Key 隔离：其它租户/伪造引用按上下文过期处理，不跨租户恢复。
func TestOpenAIBPSCompactReferenceTenantIsolation(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	upstreamCalls := 0
	h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		upstreamCalls++
		return bpsCompactSummaryResponse("summary"), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-tenant","input":"original task"}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	ref := bpsCompactRef(t, recorder.Body.Bytes())
	upstreamCalls = 0

	// 另一个 API Key 使用同一引用：查不到索引，409。
	other := `{"model":"gpt-6-astra","input":[{"type":"compaction","encrypted_content":"` + ref + `"}]}`
	c2, recorder2 := newBPSHandlerTestContext(t, other, nil)
	c2.Set("api_key", &APIKey{ID: 43})
	h.Responses(c2)
	require.Equal(t, http.StatusConflict, recorder2.Code, recorder2.Body.String())
	code, _ := decodeOpenAIBPSAPIError(t, recorder2)
	require.Equal(t, OpenAIBPSErrorContextExpired, code)
	require.Zero(t, upstreamCalls)

	// 本租户但伪造引用：同样 409（绝不凭空恢复摘要）。
	forged := `{"model":"gpt-6-astra","input":[{"type":"compaction","encrypted_content":"bpscmp_v1_forged"}]}`
	c3, recorder3 := newBPSHandlerTestContext(t, forged, nil)
	h.Responses(c3)
	require.Equal(t, http.StatusConflict, recorder3.Code)
	code, _ = decodeOpenAIBPSAPIError(t, recorder3)
	require.Equal(t, OpenAIBPSErrorContextExpired, code)
	require.Zero(t, upstreamCalls)
}

// 7 天闲置续期：每次读取都把摘要/引用索引的 TTL 重置回 7 天。
func TestOpenAIBPSCompactStateRenewsTTL(t *testing.T) {
	store, server := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	sessionKey := "session-renew"
	ref := openAIBPSCompactPrefix + "renew"
	require.NoError(t, store.SaveCompactState(ctx, 42, sessionKey, ref, openAIBPSCompactState{Summary: "summary"}))
	require.NoError(t, store.SaveCompactReference(ctx, 42, ref, sessionKey))
	stateKey := OpenAIBPSCompactRedisKey(42, sessionKey, ref)
	referenceKey := OpenAIBPSCompactReferenceRedisKey(42, ref)

	server.FastForward(6 * 24 * time.Hour)
	_, err := store.LoadCompactState(ctx, 42, sessionKey, ref)
	require.NoError(t, err)
	require.Greater(t, server.TTL(stateKey), 6*24*time.Hour)
	_, err = store.LoadCompactReference(ctx, 42, ref)
	require.NoError(t, err)
	require.Greater(t, server.TTL(referenceKey), 6*24*time.Hour)

	// 再闲置 6 天：两个键都被上一次读取续期，仍然可读。
	server.FastForward(6 * 24 * time.Hour)
	_, err = store.LoadCompactReference(ctx, 42, ref)
	require.NoError(t, err)
	_, err = store.LoadCompactState(ctx, 42, sessionKey, ref)
	require.NoError(t, err)

	// 闲置超过 7 天后过期：显式返回 expired，不返回空摘要。
	server.FastForward(8 * 24 * time.Hour)
	_, err = store.LoadCompactState(ctx, 42, sessionKey, ref)
	require.ErrorIs(t, err, ErrOpenAIBPSCompactExpired)
}

// 流式压缩：只交付终态压缩 item 与引用，摘要文本不进入任何事件。
func TestOpenAIBPSCompactStreamReturnsOnlyReference(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		return bpsCompactSummaryResponse("stream summary text"), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-compact-stream","input":"original task","stream":true}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.created")
	require.Contains(t, forwarded, "event: response.output_item.done")
	require.Contains(t, forwarded, "event: response.completed")
	require.Contains(t, forwarded, openAIBPSCompactPrefix)
	require.NotContains(t, forwarded, "stream summary text")
	completed := bpsSSEData(t, forwarded, "response.completed")
	require.Equal(t, "compaction", gjson.Get(completed, "response.output.0.type").String())
	require.Equal(t, "response.compaction", gjson.Get(completed, "response.object").String())
}

// 压缩上游走 SSE：摘要文本增量被完全抑制，只交付终态压缩引用。
func TestOpenAIBPSCompactUpstreamSSE(t *testing.T) {
	enableOpenAIBPSForTest(t)
	store, _ := newOpenAIBPSTestStore(t)
	message := bpsStructuredMessage("msg_summary_sse", "sse summary text")
	stream := bpsTestSSEEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_compact_sse", "status": "in_progress"}}) +
		bpsTestSSEEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_summary_sse", "content": []any{}}}) +
		bpsTestSSEEvent("response.output_text.delta", map[string]any{"item_id": "msg_summary_sse", "output_index": 0, "delta": "partial summary"}) +
		bpsTestSSEEvent("response.output_item.done", map[string]any{"output_index": 0, "item": message}) +
		bpsTestSSEEvent("response.completed", map[string]any{"response": bpsTestCompletedResponse(message)})
	h := bpsCompactTestHandler(t, store, func(*http.Request) (*http.Response, error) {
		return bpsTestResponse(http.StatusOK, "text/event-stream", "", stream), nil
	})
	body := `{"model":"gpt-6-astra","session_id":"conv-compact-sse-up","input":"original task","stream":true}`
	c, recorder := newBPSHandlerTestContext(t, body, nil)
	h.Compact(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	forwarded := recorder.Body.String()
	require.Contains(t, forwarded, "event: response.created")
	require.Contains(t, forwarded, "event: response.completed")
	require.Contains(t, forwarded, openAIBPSCompactPrefix)
	require.NotContains(t, forwarded, "partial summary")
	require.NotContains(t, forwarded, "sse summary text")
	completed := bpsSSEData(t, forwarded, "response.completed")
	require.Equal(t, "compaction", gjson.Get(completed, "response.output.0.type").String())
	state, err := store.LoadCompactState(context.Background(), 42, mustBPSSessionKey(t, 42, body),
		gjson.Get(completed, "response.output.0.encrypted_content").String())
	require.NoError(t, err)
	require.Equal(t, "sse summary text", state.Summary)
}
