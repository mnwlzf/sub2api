package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// function 默认信封是 name/arguments，且允许 JSON 字符串多层包裹。
func TestOpenAIBPSEnvelopeFunctionRoundTrip(t *testing.T) {
	envelope, err := openAIBPSEncodeFunctionEnvelope("functions.exec", map[string]any{"command": "pwd"})
	require.NoError(t, err)
	require.Equal(t, "functions.exec", envelope["name"])
	require.Equal(t, map[string]any{"command": "pwd"}, envelope["arguments"])

	decoded, err := openAIBPSDecodeTransportEnvelope(openAIBPSJSON(envelope))
	require.NoError(t, err)
	name, err := openAIBPSEnvelopeName(decoded)
	require.NoError(t, err)
	require.Equal(t, "functions.exec", name)
	args, err := openAIBPSEnvelopeArguments(decoded)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"command": "pwd"}, args)

	// arguments/args 同时出现是冲突（解码层保留字段，转换层判定）。
	if _, err := openAIBPSEnvelopeArguments(map[string]any{"arguments": map[string]any{}, "args": map[string]any{}}); err == nil {
		t.Fatal("conflicting argument fields must be rejected")
	}

	// 兼容 tool/args 别名。
	aliased, err := openAIBPSDecodeTransportEnvelope(map[string]any{"tool": "functions.exec", "args": map[string]any{"command": "ls"}})
	require.NoError(t, err)
	name, err = openAIBPSEnvelopeName(aliased)
	require.NoError(t, err)
	require.Equal(t, "functions.exec", name)
	args, err = openAIBPSEnvelopeArguments(aliased)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"command": "ls"}, args)

	// Markdown 围栏（可选 json 语言标记）与短前缀说明都允许剥离。
	for _, raw := range []string{
		"```json\n" + openAIBPSJSON(envelope) + "\n```",
		"Here is the call:\n```json\n" + openAIBPSJSON(envelope) + "\n```",
		"Calling now: " + openAIBPSJSON(envelope),
	} {
		decoded, err := openAIBPSDecodeTransportEnvelope(raw)
		require.NoError(t, err, raw)
		name, err := openAIBPSEnvelopeName(decoded)
		require.NoError(t, err)
		require.Equal(t, "functions.exec", name)
	}
}

func TestOpenAIBPSEnvelopeRejectsAmbiguity(t *testing.T) {
	for _, value := range []any{
		map[string]any{"name": "a", "tool": "b"},
		// JSON 之后还有可执行代码：绝不能求值/忽略尾随内容。
		`{"name":"functions.exec","arguments":{"command":"pwd"}}; doSomething()`,
		// OfficeJS 原文不是信封。
		`await Excel.run(function(context) { return context.sync(); })`,
		``,
		strings.Repeat("x", 4096),
	} {
		_, err := openAIBPSDecodeTransportEnvelope(value)
		require.Error(t, err, "%v", value)
	}
	_, err := openAIBPSDecodeTransportEnvelope([]any{map[string]any{"name": "a"}})
	require.Error(t, err)
}

// run_officejs 载体允许被多包一层（不同上游版本），但最多两层。
func TestOpenAIBPSEnvelopeNestedRunOfficeJS(t *testing.T) {
	inner := map[string]any{"name": "functions.exec", "arguments": map[string]any{"command": "pwd"}}
	nativeArgs := map[string]any{"code": openAIBPSJSON(inner)}
	outer := map[string]any{"name": "run_officejs", "arguments": openAIBPSJSON(nativeArgs)}
	decoded, err := openAIBPSDecodeTransportEnvelope(openAIBPSJSON(outer))
	require.NoError(t, err)
	name, err := openAIBPSEnvelopeName(decoded)
	require.NoError(t, err)
	require.Equal(t, "functions.exec", name)

	// 超过两层包装拒绝（避免无限解包/资源放大）。
	tooDeep := any(inner)
	for i := 0; i < 4; i++ {
		tooDeep = map[string]any{"name": "run_officejs", "arguments": openAIBPSJSON(map[string]any{"code": openAIBPSJSON(tooDeep)})}
	}
	_, err = openAIBPSDecodeTransportEnvelope(tooDeep)
	require.Error(t, err)
}

// custom 默认信封是 summary=codex2api.custom/<完整工具名> + code 原文；
// 编码器与解码器必须互为逆运算，且 summary 名字必须精确。
func TestOpenAIBPSCustomEnvelope(t *testing.T) {
	envelope, err := openAIBPSEncodeCustomEnvelope("apply_patch", "*** Begin Patch\n*** End Patch")
	require.NoError(t, err)
	require.Equal(t, openAIBPSCustomTransportPrefix+"apply_patch", envelope["summary"])
	require.Equal(t, "*** Begin Patch\n*** End Patch", envelope["code"])

	decoded, marked, err := openAIBPSCustomTransportEnvelope(envelope)
	require.NoError(t, err)
	require.True(t, marked)
	require.Equal(t, map[string]any{"name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"}, decoded)

	// 普通 run_officejs 参数不是 custom 载体。
	_, marked, err = openAIBPSCustomTransportEnvelope(map[string]any{"code": "{}"})
	require.NoError(t, err)
	require.False(t, marked)

	for _, summary := range []string{
		openAIBPSCustomTransportPrefix,
		openAIBPSCustomTransportPrefix + "bad name",
		openAIBPSCustomTransportPrefix + "bad/name",
	} {
		_, marked, err = openAIBPSCustomTransportEnvelope(map[string]any{"summary": summary, "code": "x"})
		require.True(t, marked)
		require.Error(t, err, summary)
	}
	_, marked, err = openAIBPSCustomTransportEnvelope(map[string]any{"summary": openAIBPSCustomTransportPrefix + "tool", "code": 7})
	require.True(t, marked)
	require.Error(t, err)

	// 编码期校验：空名/空白/斜杠名、超过 1 MiB 的原文都拒绝。
	_, err = openAIBPSEncodeCustomEnvelope("", "x")
	require.Error(t, err)
	_, err = openAIBPSEncodeCustomEnvelope("bad name", "x")
	require.Error(t, err)
	_, err = openAIBPSEncodeCustomEnvelope("tool", strings.Repeat("x", openAIBPSMaxEnvelopeBytes+1))
	require.Error(t, err)
	// 解码期同样有 1 MiB 上限。
	_, marked, err = openAIBPSCustomTransportEnvelope(map[string]any{
		"summary": openAIBPSCustomTransportPrefix + "tool",
		"code":    strings.Repeat("x", openAIBPSMaxEnvelopeBytes+1),
	})
	require.True(t, marked)
	require.Error(t, err)
}

// 非法 JSON 只允许修复字符串里的裸控制字符/非法转义，绝不猜测缺失结构。
func TestOpenAIBPSEnvelopeRepairOnlyStrings(t *testing.T) {
	raw := "{\n\"name\":\"functions.exec\",\n\"arguments\":{\"command\":\"line1\nline2\"}\n}"
	decoded, err := openAIBPSDecodeTransportEnvelope(raw)
	require.NoError(t, err)
	name, err := openAIBPSEnvelopeName(decoded)
	require.NoError(t, err)
	require.Equal(t, "functions.exec", name)

	_, err = openAIBPSDecodeTransportEnvelope(`{"name":"functions.exec","arguments":{"command":"pwd"`)
	require.Error(t, err, "缺少闭合括号不得被猜测修复")
}
