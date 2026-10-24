package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func bpsTurnUser(text string) map[string]any {
	return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
}

func bpsTurnOutput(callID string) map[string]any {
	return map[string]any{"type": "function_call_output", "call_id": callID, "output": "ok"}
}

// 轮次身份是纯函数：重试相同请求得到相同 turn_id；工具结果不改变轮次，
// 只递增迭代。
func TestOpenAIBPSTurnStateRetryStable(t *testing.T) {
	user := bpsTurnUser("task")
	input := []any{user}
	turn, iteration := openAIBPSTurnState("scope-a", input, -1, "", openAIBPSCompactState{})
	require.NotEmpty(t, turn)
	require.Equal(t, 1, iteration)

	againTurn, againIteration := openAIBPSTurnState("scope-a", []any{user}, -1, "", openAIBPSCompactState{})
	require.Equal(t, turn, againTurn)
	require.Equal(t, iteration, againIteration)

	// 同一轮的工具往返不改变 turn_id，只递增迭代。
	withOutput := []any{user, bpsTurnOutput("call_1")}
	toolTurn, toolIteration := openAIBPSTurnState("scope-a", withOutput, -1, "", openAIBPSCompactState{})
	require.Equal(t, turn, toolTurn)
	require.Equal(t, 2, toolIteration)

	// 新的用户消息开启新轮次。
	nextTurn, _ := openAIBPSTurnState("scope-a", []any{user, bpsTurnOutput("call_1"), bpsTurnUser("continue")}, -1, "", openAIBPSCompactState{})
	require.NotEqual(t, turn, nextTurn)

	// 不同 scope 的同一段历史必须是不同轮次。
	otherScope, _ := openAIBPSTurnState("scope-b", input, -1, "", openAIBPSCompactState{})
	require.NotEqual(t, turn, otherScope)
}

// 压缩记录里的轮次锚点：压缩之后没有新的用户消息时保持同一轮次，并按新增
// 工具结果递增迭代基数。
func TestOpenAIBPSTurnStateCompactionAnchor(t *testing.T) {
	state := openAIBPSCompactState{TurnID: "anchor-turn", AgentIteration: 2}
	compaction := map[string]any{"type": "compaction", "encrypted_content": openAIBPSCompactPrefix + "ref"}

	turn, iteration := openAIBPSTurnState("scope", []any{compaction}, 0, "ref", state)
	require.Equal(t, "anchor-turn", turn)
	require.Equal(t, 2, iteration)

	turn, iteration = openAIBPSTurnState("scope", []any{compaction, bpsTurnOutput("call_1")}, 0, "ref", state)
	require.Equal(t, "anchor-turn", turn)
	require.Equal(t, 3, iteration)

	// 压缩之后出现新的用户消息：开启新轮次，不再沿用锚点。
	turn, iteration = openAIBPSTurnState("scope", []any{compaction, bpsTurnUser("continue")}, 0, "ref", state)
	require.NotEqual(t, "anchor-turn", turn)
	require.Equal(t, 1, iteration)
}

// 旧压缩记录没有 turn_id：按 scope+ref 派生稳定值（跨实例一致）。
func TestOpenAIBPSTurnStateLegacyCompaction(t *testing.T) {
	compaction := map[string]any{"type": "compaction", "encrypted_content": openAIBPSCompactPrefix + "legacy"}
	turn, iteration := openAIBPSTurnState("scope", []any{compaction}, 0, "ref-1", openAIBPSCompactState{Summary: "legacy"})
	require.NotEmpty(t, turn)
	require.Equal(t, 1, iteration)

	again, _ := openAIBPSTurnState("scope", []any{compaction}, 0, "ref-1", openAIBPSCompactState{Summary: "legacy"})
	require.Equal(t, turn, again)

	otherRef, _ := openAIBPSTurnState("scope", []any{compaction}, 0, "ref-2", openAIBPSCompactState{Summary: "legacy"})
	require.NotEqual(t, turn, otherRef)

	otherScope, _ := openAIBPSTurnState("scope-2", []any{compaction}, 0, "ref-1", openAIBPSCompactState{Summary: "legacy"})
	require.NotEqual(t, turn, otherScope)
}

// 没有用户消息时退化为首条 item 派生，重试仍稳定。
func TestOpenAIBPSTurnStateWithoutUserMessage(t *testing.T) {
	assistant := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hi"}}}
	turn, iteration := openAIBPSTurnState("scope", []any{assistant}, -1, "", openAIBPSCompactState{})
	require.NotEmpty(t, turn)
	require.Equal(t, 1, iteration)

	sameTurn, sameIteration := openAIBPSTurnState("scope", []any{assistant, bpsTurnOutput("call_1")}, -1, "", openAIBPSCompactState{})
	require.Equal(t, turn, sameTurn)
	require.Equal(t, 2, sameIteration)
}
