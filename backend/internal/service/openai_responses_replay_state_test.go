package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStripOpenAIResponsesForeignReplayState(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","store":false,"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"reasoning","id":"rs_1","encrypted_content":"blob","summary":[{"type":"summary_text","text":"think"}]},
		{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello"}],"status":"completed"},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":"ok"},
		{"type":"item_reference","id":"rs_2"},
		{"type":"compaction","encrypted_content":"cblob"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}
	]}`)
	out, changed, stats, err := StripOpenAIResponsesForeignReplayState(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 1, stats.DroppedReasoning)
	require.Equal(t, 1, stats.DroppedReferences)
	require.Equal(t, 1, stats.DroppedCompaction)
	require.Equal(t, 2, stats.StrippedItemIDs)

	input := gjson.GetBytes(out, "input")
	require.True(t, input.IsArray())
	require.Equal(t, 5, len(input.Array()))

	// reasoning / item_reference / compaction 整条消失。
	require.False(t, gjson.GetBytes(out, `input.#(type=="reasoning")`).Exists())
	require.False(t, gjson.GetBytes(out, `input.#(type=="item_reference")`).Exists())
	require.False(t, gjson.GetBytes(out, `input.#(type=="compaction")`).Exists())

	// 其余 item 保留内容与 call_id，但去掉 id。
	require.Equal(t, "call_1", gjson.GetBytes(out, `input.#(type=="function_call").call_id`).String())
	require.Equal(t, "shell", gjson.GetBytes(out, `input.#(type=="function_call").name`).String())
	require.False(t, gjson.GetBytes(out, `input.#(type=="function_call").id`).Exists())
	require.False(t, gjson.GetBytes(out, `input.#(type=="message").id`).Exists())
	require.Equal(t, "ok", gjson.GetBytes(out, `input.#(type=="function_call_output").output`).String())
	require.False(t, gjson.GetBytes(out, `input.#(type=="function_call_output").id`).Exists())
}

func TestStripOpenAIResponsesForeignReplayStateNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out, changed, stats, err := StripOpenAIResponsesForeignReplayState(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.False(t, stats.Changed())
	require.Equal(t, body, out)
}

func TestStripOpenAIResponsesForeignReplayStateSkipsNonArrayInput(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":"hello"}`)
	out, changed, _, err := StripOpenAIResponsesForeignReplayState(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, out)
}

func TestStripOpenAIResponsesForeignReplayStateInvalidJSON(t *testing.T) {
	// 非法 JSON 不是本函数的职责：上游链路此前已校验，这里原样返回不报错。
	body := []byte(`{"input":`)
	out, changed, _, err := StripOpenAIResponsesForeignReplayState(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, out)
}

func TestAccountIsOpenAIResponsesStripForeignReplayStateEnabled(t *testing.T) {
	tests := []struct {
		name string
		acct *Account
		want bool
	}{
		{
			name: "开关打开",
			acct: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Extra:    map[string]any{"openai_responses_strip_foreign_replay_state": true},
			},
			want: true,
		},
		{
			name: "开关关闭",
			acct: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Extra:    map[string]any{"openai_responses_strip_foreign_replay_state": false},
			},
			want: false,
		},
		{
			name: "字段缺失",
			acct: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{}},
			want: false,
		},
		{
			name: "非 OpenAI 平台不生效",
			acct: &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeAPIKey,
				Extra:    map[string]any{"openai_responses_strip_foreign_replay_state": true},
			},
			want: false,
		},
		{
			name: "nil 账号",
			acct: nil,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.acct.IsOpenAIResponsesStripForeignReplayStateEnabled())
		})
	}
}

func TestOpenAIResponsesCompatibilityStripsForeignReplayState(t *testing.T) {
	acct := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_responses_strip_foreign_replay_state": true},
	}
	body := []byte(`{"model":"gpt-6-astra","store":false,"input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"blob"},
		{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello"}],"status":"completed"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}
	]}`)
	out, changed, err := normalizeOpenAIResponsesCompatibilityBodyWithOptions(body, acct, openAIResponsesCompatibilityOptions{})
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, `input.#(type=="reasoning")`).Exists())
	require.False(t, gjson.GetBytes(out, `input.#(type=="message").id`).Exists())

	// 有状态请求（store 非 false）不剥离。
	stateful := []byte(`{"model":"gpt-6-astra","store":true,"input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"blob"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}
	]}`)
	outStateful, _, err := normalizeOpenAIResponsesCompatibilityBodyWithOptions(stateful, acct, openAIResponsesCompatibilityOptions{})
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(outStateful, `input.#(type=="reasoning")`).Exists())

	// 开关关闭的账号不剥离。
	off := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{}}
	outOff, _, err := normalizeOpenAIResponsesCompatibilityBodyWithOptions(body, off, openAIResponsesCompatibilityOptions{})
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(outOff, `input.#(type=="reasoning")`).Exists())
}
