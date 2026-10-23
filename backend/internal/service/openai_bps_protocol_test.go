package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func bpsRequireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, code, appErr.Reason, "error: %v", err)
}

func bpsProtocolTestPrepare(t *testing.T, store *OpenAIBPSSessionStore, sessionKey string, body map[string]any) (*openAIBPSPreparedRequest, error) {
	t.Helper()
	account := bpsTestAccount(7, "token-7", "ws-7")
	return prepareOpenAIBPSRequest(context.Background(), store, account, 42, sessionKey, []byte(openAIBPSJSON(body)))
}

func bpsTestFunctionTool(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"name": name,
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"command": map[string]any{"type": "string"}},
			"required":             []any{"command"},
			"additionalProperties": false,
		},
	}
}

func bpsTestNativeCall(callID, catalogName string, arguments map[string]any) map[string]any {
	envelope, _ := openAIBPSEncodeFunctionEnvelope(catalogName, arguments)
	return map[string]any{
		"type":      "function_call",
		"id":        "fc_" + callID,
		"call_id":   callID,
		"name":      openAIBPSNativeToolName,
		"arguments": openAIBPSJSON(map[string]any{"code": openAIBPSJSON(envelope)}),
		"status":    "completed",
	}
}

func bpsTestCompletedResponse(output ...any) map[string]any {
	return map[string]any{
		"id":     "resp_test",
		"object": "response",
		"status": "completed",
		"model":  "gpt-6-astra",
		"output": output,
		"usage":  map[string]any{"input_tokens": 10, "output_tokens": 2},
	}
}

func TestOpenAIBPSCollectToolsCatalog(t *testing.T) {
	tools := map[string]openAIBPSTool{}
	var catalog []any
	nested := []any{map[string]any{
		"type": "namespace",
		"name": "functions",
		"tools": []any{
			bpsTestFunctionTool("exec"),
			map[string]any{"type": "custom", "name": "apply_patch", "format": map[string]any{"type": "text"}},
		},
	}}
	require.NoError(t, openAIBPSCollectTools(nested, "", tools, &catalog, 0))
	require.Len(t, tools, 2)
	require.Len(t, catalog, 2)
	require.Contains(t, tools, "functions.exec")
	require.Equal(t, "functions", tools["functions.exec"].Namespace)
	require.Equal(t, "function", tools["functions.exec"].Kind)
	require.NotNil(t, tools["functions.exec"].Schema)
	require.Equal(t, "functions.exec", gjson.Get(openAIBPSJSON(catalog[0]), "name").String())
	require.True(t, gjson.Get(openAIBPSJSON(catalog[0]), "parameters").Exists())
	require.Equal(t, "custom", tools["functions.apply_patch"].Kind)
	require.Equal(t, "custom", gjson.Get(openAIBPSJSON(catalog[1]), "type").String())

	// 重复且完全一致的声明合并（目录只出现一次）。
	merged := map[string]openAIBPSTool{}
	var mergedCatalog []any
	require.NoError(t, openAIBPSCollectTools([]any{bpsTestFunctionTool("exec"), bpsTestFunctionTool("exec")}, "", merged, &mergedCatalog, 0))
	require.Len(t, merged, 1)
	require.Len(t, mergedCatalog, 1)

	// 同名但声明不同：冲突必须 400，绝不静默覆盖。
	conflicting := map[string]openAIBPSTool{}
	var conflictingCatalog []any
	other := bpsTestFunctionTool("exec")
	other["description"] = "different"
	err := openAIBPSCollectTools([]any{bpsTestFunctionTool("exec"), other}, "", conflicting, &conflictingCatalog, 0)
	bpsRequireErrorCode(t, err, OpenAIBPSErrorInvalidRequest)

	// 未声明/不支持的客户端工具类型、非数组、无名工具。
	for _, raw := range []any{
		[]any{map[string]any{"type": "web_search"}},
		[]any{map[string]any{"type": "function", "name": ""}},
		[]any{"not-an-object"},
		map[string]any{"type": "function", "name": "x"},
	} {
		bad := map[string]openAIBPSTool{}
		var badCatalog []any
		require.Error(t, openAIBPSCollectTools(raw, "", bad, &badCatalog, 0), "%v", raw)
	}

	// function schema 外部 $ref 在声明阶段拒绝。
	externalRef := map[string]openAIBPSTool{}
	var externalCatalog []any
	err = openAIBPSCollectTools([]any{map[string]any{
		"type":       "function",
		"name":       "remote",
		"parameters": map[string]any{"$ref": "file:///etc/passwd"},
	}}, "", externalRef, &externalCatalog, 0)
	bpsRequireErrorCode(t, err, OpenAIBPSErrorInvalidRequest)

	// 无 parameters 时使用空对象 schema。
	defaulted := map[string]openAIBPSTool{}
	var defaultedCatalog []any
	require.NoError(t, openAIBPSCollectTools([]any{map[string]any{"type": "function", "name": "noargs"}}, "", defaulted, &defaultedCatalog, 0))
	require.NoError(t, defaulted["noargs"].Schema.Validate(map[string]any{}))

	// 超过 256 个工具拒绝。
	many := make([]any, 0, 257)
	for i := 0; i < 257; i++ {
		many = append(many, map[string]any{"type": "function", "name": "tool" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + strings.TrimSpace(strings.Repeat("", i))})
	}
	tooMany := map[string]openAIBPSTool{}
	var tooManyCatalog []any
	// 生成唯一名字，避免同名冲突干扰数量断言。
	for i := range many {
		many[i] = map[string]any{"type": "function", "name": "tool_" + string(rune('a'+i%26)) + "_" + string(rune('a'+(i/26)%26)) + "_" + string(rune('a'+i%26))}
	}
	err = openAIBPSCollectTools(many, "", tooMany, &tooManyCatalog, 0)
	require.Error(t, err)

	// 命名空间嵌套超过 8 层拒绝。
	deep := map[string]any{"type": "function", "name": "leaf"}
	for i := 0; i < 9; i++ {
		deep = map[string]any{"type": "namespace", "name": "ns", "tools": []any{deep}}
	}
	deepTools := map[string]openAIBPSTool{}
	var deepCatalog []any
	require.Error(t, openAIBPSCollectTools([]any{deep}, "", deepTools, &deepCatalog, 0))
}

func TestOpenAIBPSPrepareRequestProtocolAndCatalog(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared, err := bpsProtocolTestPrepare(t, store, "session-a", map[string]any{
		"model":        "gpt-6-astra",
		"instructions": "client rules",
		"input":        "hello",
		"stream":       true,
		"tools":        []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	require.True(t, prepared.Stream)
	require.Equal(t, "gpt-6-astra", prepared.ClientModel)
	require.Equal(t, "gpt-6-astra", prepared.UpstreamModel)
	require.Len(t, prepared.Tools, 1)
	require.Len(t, prepared.Catalog, 1)

	body := prepared.Body
	require.False(t, gjson.GetBytes(body, "tools").Exists(), "客户端 tools 声明不得直发上游")
	require.False(t, gjson.GetBytes(body, "store").Bool())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.Equal(t, "explicit", gjson.GetBytes(body, "model_selection").String())
	require.Equal(t, "medium", gjson.GetBytes(body, "reasoning_effort").String())
	require.Equal(t, "session-a", gjson.GetBytes(body, "prompt_cache_key").String())
	require.NotEmpty(t, gjson.GetBytes(body, "metadata.task_id").String())

	input := gjson.GetBytes(body, "input").Array()
	require.Len(t, input, 3)
	require.Equal(t, "developer", input[0].Get("role").String())
	require.Equal(t, "client rules", input[0].Get("content.0.text").String())
	require.Equal(t, "developer", input[1].Get("role").String())
	protocol := input[1].Get("content.0.text").String()
	require.Contains(t, protocol, "run_officejs")
	require.Contains(t, protocol, `"name":"exec"`)
	require.Equal(t, "user", input[2].Get("role").String())
	require.Equal(t, "hello", input[2].Get("content.0.text").String())

	// input 里的 additional_tools 也要进入目录，且不透传给上游。
	withAdditional, err := bpsProtocolTestPrepare(t, store, "session-b", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "hello"),
			map[string]any{"type": "additional_tools", "tools": []any{bpsTestFunctionTool("late_tool")}},
		},
	})
	require.NoError(t, err)
	require.Contains(t, withAdditional.Tools, "late_tool")
	require.NotContains(t, string(withAdditional.Body), "additional_tools")

	// tool_choice=required：协议追加强制说明。
	required, err := bpsProtocolTestPrepare(t, store, "session-c", map[string]any{
		"model":       "gpt-6-astra",
		"input":       "hello",
		"tools":       []any{bpsTestFunctionTool("exec")},
		"tool_choice": "required",
	})
	require.NoError(t, err)
	require.True(t, required.RequireTool)
	require.Contains(t, string(required.Body), "must call one of the listed tools")

	// tool_choice 指定工具：目录收敛到该工具。
	choice, err := bpsProtocolTestPrepare(t, store, "session-d", map[string]any{
		"model":       "gpt-6-astra",
		"input":       "hello",
		"tools":       []any{bpsTestFunctionTool("exec"), bpsTestFunctionTool("other")},
		"tool_choice": map[string]any{"type": "function", "name": "exec"},
	})
	require.NoError(t, err)
	require.Len(t, choice.Tools, 1)
	require.True(t, choice.RequireTool)
	require.Contains(t, choice.Tools, "exec")

	// tool_choice=none：不收集工具，上游收到无工具协议。
	none, err := bpsProtocolTestPrepare(t, store, "session-e", map[string]any{
		"model":       "gpt-6-astra",
		"input":       "hello",
		"tools":       []any{bpsTestFunctionTool("exec")},
		"tool_choice": "none",
	})
	require.NoError(t, err)
	require.Empty(t, none.Tools)
	require.NotContains(t, string(none.Body), "Tool catalog")
	require.Contains(t, string(none.Body), "without an Excel workbook")

	// tool_choice 指向未声明工具 / 不支持的值。
	for _, tc := range []map[string]any{
		{"model": "gpt-6-astra", "input": "hi", "tools": []any{bpsTestFunctionTool("exec")}, "tool_choice": map[string]any{"name": "missing"}},
		{"model": "gpt-6-astra", "input": "hi", "tool_choice": "sometimes"},
		{"model": "gpt-6-astra", "input": "hi", "tool_choice": "required"},
	} {
		_, err := bpsProtocolTestPrepare(t, store, "session-f", tc)
		bpsRequireErrorCode(t, err, OpenAIBPSErrorInvalidRequest)
	}
}

func TestOpenAIBPSPrepareRequestRejectsUnsupportedInputs(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	for _, body := range []map[string]any{
		{"model": "gpt-6-astra", "input": "hi", "previous_response_id": "resp_1"},
		{"model": "gpt-6-astra", "input": "hi", "background": true},
		{"input": "hi"},
		{"model": "gpt-6-astra", "input": 42},
		{"model": "gpt-6-astra", "input": []any{"not-an-object"}},
		{"model": "gpt-6-astra", "input": []any{map[string]any{"type": "item_reference", "id": "x"}}},
		{"model": "gpt-6-astra", "input": []any{map[string]any{"type": "compaction", "encrypted_content": "bpscmp_v1_x"}}},
		{"model": "gpt-6-astra", "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "https://x"}}}}},
		{"model": "gpt-6-astra", "input": "hi", "reasoning": map[string]any{"effort": "max"}},
		{"model": "gpt-6-astra", "input": "hi", "reasoning": "high"},
		{"model": "gpt-6-astra", "input": "hi", "reasoning_effort": 7},
	} {
		_, err := bpsProtocolTestPrepare(t, store, "session-r", body)
		bpsRequireErrorCode(t, err, OpenAIBPSErrorInvalidRequest)
	}
	// x-high 别名归一化为 xhigh。
	prepared, err := bpsProtocolTestPrepare(t, store, "session-r", map[string]any{
		"model": "gpt-6-astra", "input": "hi", "reasoning": map[string]any{"effort": "x-high"},
	})
	require.NoError(t, err)
	require.Equal(t, "xhigh", gjson.GetBytes(prepared.Body, "reasoning_effort").String())
}

func TestOpenAIBPSConvertToolCallFunction(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared, err := bpsProtocolTestPrepare(t, store, "session-convert", map[string]any{
		"model": "gpt-6-astra",
		"input": "hi",
		"tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)

	client, tool, err := openAIBPSConvertToolCall(bpsTestNativeCall("call_1", "exec", map[string]any{"command": "pwd"}), prepared)
	require.NoError(t, err)
	require.NotNil(t, tool)
	require.Equal(t, "function_call", client["type"])
	require.Equal(t, "fc_call_1", client["id"])
	require.Equal(t, "call_1", client["call_id"])
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "completed", client["status"])
	require.JSONEq(t, `{"command":"pwd"}`, client["arguments"].(string))

	// 双层 JSON 编码的信封仍然可以解出。
	native := bpsTestNativeCall("call_2", "exec", map[string]any{"command": "ls"})
	native["arguments"] = openAIBPSJSON(map[string]any{"code": openAIBPSJSON(openAIBPSJSON(map[string]any{"name": "exec", "arguments": map[string]any{"command": "ls"}}))})
	_, _, err = openAIBPSConvertToolCall(native, prepared)
	require.NoError(t, err)

	// 未知工具 / 未声明的服务端工具 / schema 不匹配 / 非法身份 / 尾随代码。
	for name, native := range map[string]map[string]any{
		"unknown tool": bpsTestNativeCall("call_3", "missing", map[string]any{}),
		"server tool":  {"type": "function_call", "id": "x", "call_id": "x", "name": "web_search", "arguments": "{}"},
		"schema":       bpsTestNativeCall("call_4", "exec", map[string]any{"command": 123}),
		"extra arg":    bpsTestNativeCall("call_5", "exec", map[string]any{"command": "pwd", "extra": true}),
		"identity":     {"type": "function_call", "name": "run_officejs", "arguments": openAIBPSJSON(map[string]any{"code": openAIBPSJSON(map[string]any{"name": "exec", "arguments": map[string]any{"command": "pwd"}})})},
		"trailing":     {"type": "function_call", "id": "fc_x", "call_id": "x", "name": "run_officejs", "arguments": openAIBPSJSON(map[string]any{"code": `{"name":"exec","arguments":{"command":"pwd"}}; run()`})},
	} {
		_, _, err := openAIBPSConvertToolCall(native, prepared)
		require.Error(t, err, name)
	}
	_, _, err = openAIBPSConvertToolCall(bpsTestNativeCall("call_6", "exec", map[string]any{"command": "pwd"}), &openAIBPSPreparedRequest{})
	bpsRequireErrorCode(t, err, OpenAIBPSErrorUnsupportedTool)

	// 信封 1 MiB 上限：超长参数文本拒绝。
	big := strings.Repeat("x", openAIBPSMaxEnvelopeBytes+1)
	bigNative := map[string]any{
		"type":      "function_call",
		"id":        "fc_big",
		"call_id":   "big",
		"name":      "run_officejs",
		"arguments": openAIBPSJSON(map[string]any{"code": `{"name":"exec","arguments":"` + big + `"}`}),
	}
	_, _, err = openAIBPSConvertToolCall(bigNative, prepared)
	require.Error(t, err)
}

func TestOpenAIBPSConvertToolCallCustomAndPlan(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	plan := map[string]any{
		"type": "function",
		"name": "update_plan",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"step":   map[string]any{"type": "string"},
							"status": map[string]any{"enum": []any{"pending", "in_progress", "completed"}},
						},
						"required": []any{"step", "status"},
					},
				},
			},
			"required": []any{"plan"},
		},
	}
	prepared, err := bpsProtocolTestPrepare(t, store, "session-custom", map[string]any{
		"model": "gpt-6-astra",
		"input": "hi",
		"tools": []any{plan, map[string]any{"type": "custom", "name": "apply_patch"}},
	})
	require.NoError(t, err)

	// custom 原文透传信封。
	customNative := map[string]any{
		"type":    "function_call",
		"id":      "fc_patch",
		"call_id": "patch_1",
		"name":    "run_officejs",
		"arguments": openAIBPSJSON(map[string]any{
			"summary": openAIBPSCustomTransportPrefix + "apply_patch",
			"code":    "*** Begin Patch\n*** End Patch",
		}),
	}
	client, tool, err := openAIBPSConvertToolCall(customNative, prepared)
	require.NoError(t, err)
	require.Equal(t, "custom", tool.Kind)
	require.Equal(t, "custom_tool_call", client["type"])
	require.Equal(t, "ctc_patch_1", client["id"])
	require.Equal(t, "*** Begin Patch\n*** End Patch", client["input"])

	// 兼容旧 JSON 信封：{"name":"apply_patch","input":"..."}（含 args 别名）。
	for _, legacy := range []struct {
		envelope map[string]any
		input    string
	}{
		{map[string]any{"name": "apply_patch", "input": "legacy raw"}, "legacy raw"},
		{map[string]any{"name": "apply_patch", "args": "legacy alias"}, "legacy alias"},
	} {
		legacyNative := map[string]any{
			"type":      "function_call",
			"id":        "fc_legacy",
			"call_id":   "legacy_1",
			"name":      "run_officejs",
			"arguments": openAIBPSJSON(map[string]any{"code": openAIBPSJSON(legacy.envelope)}),
		}
		legacyClient, _, err := openAIBPSConvertToolCall(legacyNative, prepared)
		require.NoError(t, err)
		require.Equal(t, "custom_tool_call", legacyClient["type"])
		require.Equal(t, legacy.input, legacyClient["input"])
	}

	// raw 载体只能承载 custom 工具。
	functionRaw := map[string]any{
		"type":    "function_call",
		"id":      "fc_raw",
		"call_id": "raw_1",
		"name":    "run_officejs",
		"arguments": openAIBPSJSON(map[string]any{
			"summary": openAIBPSCustomTransportPrefix + "update_plan",
			"code":    "{}",
		}),
	}
	_, _, err = openAIBPSConvertToolCall(functionRaw, prepared)
	require.Error(t, err)

	// custom 信封不得携带 function arguments。
	conflicting := map[string]any{
		"type":    "function_call",
		"id":      "fc_conflict",
		"call_id": "conflict_1",
		"name":    "run_officejs",
		"arguments": openAIBPSJSON(map[string]any{
			"name":      "apply_patch",
			"input":     "raw",
			"arguments": map[string]any{},
		}),
	}
	_, _, err = openAIBPSConvertToolCall(conflicting, prepared)
	require.Error(t, err)

	// 原生 update_plan：步骤/状态别名归一化。
	planNative := map[string]any{
		"type":      "function_call",
		"id":        "native-plan",
		"call_id":   "plan-call",
		"name":      "update_plan",
		"arguments": `{"summary":"next","plan":[{"description":"fix it","status":"done"}]}`,
	}
	planClient, planTool, err := openAIBPSConvertToolCall(planNative, prepared)
	require.NoError(t, err)
	require.Equal(t, "update_plan", planTool.Name)
	require.JSONEq(t, `{"plan":[{"step":"fix it","status":"completed"}],"explanation":"next"}`, planClient["arguments"].(string))

	// 冲突别名 / 未知状态 / 缺少 plan 数组都要拒绝。
	for _, args := range []string{
		`{"plan":[{"step":"a","title":"b","status":"pending"}]}`,
		`{"plan":[{"step":"a","status":"unknown"}]}`,
		`{"plan":"not-an-array"}`,
	} {
		native := map[string]any{"type": "function_call", "id": "fc_plan", "call_id": "plan_bad", "name": "update_plan", "arguments": args}
		_, _, err := openAIBPSConvertToolCall(native, prepared)
		require.Error(t, err, args)
	}

	// 未声明 update_plan 时不得输出。
	withoutPlan, err := bpsProtocolTestPrepare(t, store, "session-noplan", map[string]any{
		"model": "gpt-6-astra", "input": "hi", "tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	_, _, err = openAIBPSConvertToolCall(planNative, withoutPlan)
	bpsRequireErrorCode(t, err, OpenAIBPSErrorUnsupportedTool)
}

func TestOpenAIBPSTransformResponseSingleToolAndState(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared, err := bpsProtocolTestPrepare(t, store, "session-transform", map[string]any{
		"model": "gpt-6-astra",
		"input": "hi",
		"tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	native := bpsTestNativeCall("call_t1", "exec", map[string]any{"command": "pwd"})
	original := openAIBPSJSON(native)
	reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "ciphertext", "summary": []any{}}
	response, err := openAIBPSTransformResponse(context.Background(), store, prepared, bpsTestCompletedResponse(reasoning, native))
	require.NoError(t, err)
	output := response["output"].([]any)
	require.Len(t, output, 2)
	require.Equal(t, "reasoning", output[0].(map[string]any)["type"])
	client := output[1].(map[string]any)
	require.Equal(t, "function_call", client["type"])
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "gpt-6-astra", response["model"])

	// Redis 里保存了完整原始 item + 工具目录快照。
	saved, err := store.LoadToolItem(context.Background(), 42, "session-transform", "call_t1")
	require.NoError(t, err)
	require.Equal(t, "call_t1", saved.CallID)
	require.Equal(t, "fc_call_t1", saved.ItemID)
	require.Equal(t, "exec", saved.Name)
	require.Equal(t, "function", saved.Kind)
	require.JSONEq(t, original, openAIBPSJSON(saved.Native))
	require.NotEmpty(t, saved.Catalog)
	require.JSONEq(t, `{"command":"pwd"}`, string(saved.Arguments))

	// 一次最多交付一个工具调用。
	twoTools := bpsTestCompletedResponse(
		bpsTestNativeCall("call_a", "exec", map[string]any{"command": "pwd"}),
		bpsTestNativeCall("call_b", "exec", map[string]any{"command": "ls"}),
	)
	_, err = openAIBPSTransformResponse(context.Background(), store, prepared, twoTools)
	bpsRequireErrorCode(t, err, OpenAIBPSErrorParallelTools)

	// tool_choice=required 但上游只给文本。
	requiredPrepared, err := bpsProtocolTestPrepare(t, store, "session-required", map[string]any{
		"model": "gpt-6-astra", "input": "hi", "tools": []any{bpsTestFunctionTool("exec")}, "tool_choice": "required",
	})
	require.NoError(t, err)
	_, err = openAIBPSTransformResponse(context.Background(), store, requiredPrepared, bpsTestCompletedResponse(openAIBPSMessage("assistant", "no tool")))
	bpsRequireErrorCode(t, err, OpenAIBPSErrorToolRequired)

	// 未声明/未知条目与缺失 output 都要失败。
	_, err = openAIBPSTransformResponse(context.Background(), store, prepared, bpsTestCompletedResponse(map[string]any{"type": "web_search_call", "id": "ws"}))
	bpsRequireErrorCode(t, err, OpenAIBPSErrorUnsupportedTool)
	_, err = openAIBPSTransformResponse(context.Background(), store, prepared, map[string]any{"id": "resp"})
	bpsRequireErrorCode(t, err, OpenAIBPSErrorInvalidResponse)
	_, err = openAIBPSTransformResponse(context.Background(), store, prepared, bpsTestCompletedResponse(bpsTestNativeCall("call_unknown", "missing", map[string]any{})))
	bpsRequireErrorCode(t, err, OpenAIBPSErrorUnsupportedTool)
}

func TestOpenAIBPSReplayRestoresNativeItems(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared, err := bpsProtocolTestPrepare(t, store, "session-replay", map[string]any{
		"model": "gpt-6-astra",
		"input": "hi",
		"tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	native := bpsTestNativeCall("call_r1", "exec", map[string]any{"command": "pwd"})
	original := openAIBPSJSON(native)
	transformed, err := openAIBPSTransformResponse(context.Background(), store, prepared, bpsTestCompletedResponse(native))
	require.NoError(t, err)
	client := transformed["output"].([]any)[0].(map[string]any)

	// 第二轮：客户端回传工具调用 + 结果，网关恢复上游原始 item 并生成
	// 稳定 fc_ ID 的 function_call_output。
	second, err := bpsProtocolTestPrepare(t, store, "session-replay", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "hi"),
			client,
			map[string]any{"type": "function_call_output", "call_id": "call_r1", "output": "/workspace"},
		},
		"tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	items := gjson.GetBytes(second.Body, "input").Array()
	// 0 是 developer 协议消息，1 是 user，2 是恢复的原始 item，3 是结果。
	require.Len(t, items, 4)
	require.JSONEq(t, original, items[2].Raw)
	require.Equal(t, "function_call_output", items[3].Get("type").String())
	require.Equal(t, "fc_call_r1", items[3].Get("id").String())
	require.Equal(t, "call_r1", items[3].Get("call_id").String())
	require.Equal(t, "/workspace", items[3].Get("output").String())

	// 客户端只回传结果（不带 function_call）时同样要恢复原始 item，且不重复。
	outputOnly, err := bpsProtocolTestPrepare(t, store, "session-replay", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "hi"),
			map[string]any{"type": "function_call_output", "call_id": "call_r1", "output": "done"},
		},
		"tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	onlyItems := gjson.GetBytes(outputOnly.Body, "input").Array()
	require.Len(t, onlyItems, 4)
	require.JSONEq(t, original, onlyItems[2].Raw)
	require.Equal(t, "fc_call_r1", onlyItems[3].Get("id").String())

	// 状态过期（Redis 键不存在）：显式 409，绝不静默重建。
	_, err = bpsProtocolTestPrepare(t, store, "session-replay", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "hi"),
			map[string]any{"type": "function_call_output", "call_id": "call_missing", "output": "done"},
		},
	})
	bpsRequireErrorCode(t, err, OpenAIBPSErrorContextExpired)

	// 无 Redis 客户端时显式 503，不降级。
	unavailable, err := bpsProtocolTestPrepare(t, NewOpenAIBPSSessionStore(nil), "session-replay", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "hi"),
			map[string]any{"type": "function_call_output", "call_id": "call_r1", "output": "done"},
		},
	})
	require.Nil(t, unavailable)
	bpsRequireErrorCode(t, err, OpenAIBPSErrorStateUnavailable)

	// 加密 reasoning 重建：类型 + 空 summary + 原密文。
	withReasoning, err := bpsProtocolTestPrepare(t, store, "session-replay", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "hi"),
			map[string]any{"type": "reasoning", "id": "rs_x", "summary": []any{map[string]any{"type": "summary_text", "text": "visible"}}, "encrypted_content": "cipher-1"},
		},
	})
	require.NoError(t, err)
	reasoningItems := gjson.GetBytes(withReasoning.Body, "input").Array()
	require.Len(t, reasoningItems, 3)
	reasoning := reasoningItems[2]
	require.Equal(t, "reasoning", reasoning.Get("type").String())
	require.Equal(t, "cipher-1", reasoning.Get("encrypted_content").String())
	require.Equal(t, 0, len(reasoning.Get("summary").Array()))
	require.False(t, reasoning.Get("id").Exists())
}

func TestOpenAIBPSReplayUpdatePlanReceipt(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	planTool := map[string]any{
		"type": "function",
		"name": "update_plan",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"plan": map[string]any{"type": "array"}},
		},
	}
	prepared, err := bpsProtocolTestPrepare(t, store, "session-plan", map[string]any{
		"model": "gpt-6-astra", "input": "make a plan", "tools": []any{planTool},
	})
	require.NoError(t, err)
	native := map[string]any{
		"type":      "function_call",
		"id":        "native-plan",
		"call_id":   "plan-call",
		"name":      "update_plan",
		"arguments": `{"plan":[{"step":"code","status":"in_progress"}]}`,
		"status":    "completed",
	}
	transformed, err := openAIBPSTransformResponse(context.Background(), store, prepared, bpsTestCompletedResponse(native))
	require.NoError(t, err)
	client := transformed["output"].([]any)[0].(map[string]any)
	require.Equal(t, "update_plan", client["name"])

	// 成功回执 "Plan updated" 在回放时被归一化为稳定 JSON，上游只需成功信号。
	replayed, err := bpsProtocolTestPrepare(t, store, "session-plan", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "make a plan"),
			client,
			map[string]any{"type": "function_call_output", "call_id": "plan-call", "output": "Plan updated"},
		},
		"tools": []any{planTool},
	})
	require.NoError(t, err)
	items := gjson.GetBytes(replayed.Body, "input").Array()
	require.Equal(t, "update_plan", items[2].Get("name").String())
	require.Equal(t, `{"status":"ok"}`, items[3].Get("output").Raw)

	// 非成功回执原样保留。
	kept, err := bpsProtocolTestPrepare(t, store, "session-plan", map[string]any{
		"model": "gpt-6-astra",
		"input": []any{
			openAIBPSMessage("user", "make a plan"),
			map[string]any{"type": "function_call_output", "call_id": "plan-call", "output": "Plan updated but check step 2"},
		},
		"tools": []any{planTool},
	})
	require.NoError(t, err)
	keptItems := gjson.GetBytes(kept.Body, "input").Array()
	require.Equal(t, "Plan updated but check step 2", keptItems[3].Get("output").String())
}

func TestOpenAIBPSFunctionOutputIDStable(t *testing.T) {
	require.Equal(t, "fc_call_1", openAIBPSFunctionOutputID("call_1"))
	long := strings.Repeat("c", 200)
	first := openAIBPSFunctionOutputID(long)
	second := openAIBPSFunctionOutputID(long)
	require.Equal(t, first, second, "同一 call_id 必须生成稳定 ID")
	require.LessOrEqual(t, len(first), 64)
	require.True(t, strings.HasPrefix(first, "fc_"))
	require.NotEqual(t, first, openAIBPSFunctionOutputID(long+"x"))
}

// 请求准备必须是确定性的：相同输入两次准备得到完全相同的上游 body。
func TestOpenAIBPSPrepareRequestDeterministic(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	body := map[string]any{
		"model":  "gpt-6-astra",
		"input":  "hi",
		"stream": true,
		"tools":  []any{bpsTestFunctionTool("exec")},
	}
	first, err := bpsProtocolTestPrepare(t, store, "session-det", body)
	require.NoError(t, err)
	second, err := bpsProtocolTestPrepare(t, store, "session-det", body)
	require.NoError(t, err)
	require.Equal(t, string(first.Body), string(second.Body))
	// 工具目录 JSON 是稳定文本（与声明顺序一致）。
	require.Contains(t, openAIBPSJSON(first.Catalog), `"name":"exec"`)
	require.Equal(t, openAIBPSJSON(first.Catalog), openAIBPSJSON(second.Catalog))
}

// 工具目录快照保存在 Redis item 里，跨实例可读（同一个 Redis 客户端不可见
// 的实现细节不参与；这里用同一 store 验证结构完整性）。
func TestOpenAIBPSToolItemSnapshotShape(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	prepared, err := bpsProtocolTestPrepare(t, store, "session-snap", map[string]any{
		"model": "gpt-6-astra", "input": "hi", "tools": []any{bpsTestFunctionTool("exec")},
	})
	require.NoError(t, err)
	native := bpsTestNativeCall("call_snap", "exec", map[string]any{"command": "pwd"})
	_, err = openAIBPSTransformResponse(context.Background(), store, prepared, bpsTestCompletedResponse(native))
	require.NoError(t, err)
	saved, err := store.LoadToolItem(context.Background(), 42, "session-snap", "call_snap")
	require.NoError(t, err)
	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	for _, field := range []string{`"call_id":"call_snap"`, `"id":"fc_call_snap"`, `"native":`, `"catalog":`, `"arguments":`} {
		require.Contains(t, string(encoded), field)
	}
}
