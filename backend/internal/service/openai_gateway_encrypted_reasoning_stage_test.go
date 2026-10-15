package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func decodeEncryptedReasoningTestBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, decodeOpenAIJSONUseNumber([]byte(raw), &decoded))
	return decoded
}

func encryptedReasoningTestInput(t *testing.T, reqBody map[string]any) []any {
	t.Helper()
	items, ok := reqBody["input"].([]any)
	require.True(t, ok, "input must stay a JSON array")
	return items
}

func encryptedReasoningTestItem(t *testing.T, items []any, index int) map[string]any {
	t.Helper()
	item, ok := items[index].(map[string]any)
	require.True(t, ok, "input[%d] must be a JSON object", index)
	return item
}

func encryptedReasoningTestField(t *testing.T, item map[string]any, key string) string {
	t.Helper()
	value, _ := item[key].(string)
	return value
}

func encryptedReasoningTestType(t *testing.T, items []any, index int) string {
	t.Helper()
	return encryptedReasoningTestField(t, encryptedReasoningTestItem(t, items, index), "type")
}

// encryptedReasoningTestSummaryText reads summary.0.text, the field the ladder must
// preserve (even when the upstream sends an empty summary).
func encryptedReasoningTestSummaryText(t *testing.T, item map[string]any) string {
	t.Helper()
	summary, ok := item["summary"].([]any)
	require.True(t, ok, "summary must stay a JSON array")
	require.NotEmpty(t, summary, "summary must keep its placeholder entry")
	return encryptedReasoningTestField(t, encryptedReasoningTestItem(t, summary, 0), "text")
}

// S1: strip encrypted_content, keep the reasoning item's id and summary.
func TestOpenAIEncryptedReasoningStageOneKeepsIDAndSummary(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
		{"type":"reasoning","id":"rs_6ad05224","status":"completed","summary":[{"type":"summary_text","text":"keep"}],"encrypted_content":"blob"},
		{"type":"message","content":[{"type":"input_text","text":"hi"}]}]}`)

	action, changed := applyOpenAIEncryptedReasoningStage(reqBody, openAIEncryptedReasoningStageDropEncryptedContent)
	require.True(t, changed)
	require.Equal(t, openAIEncryptedReasoningActionDropEncryptedContent, action)

	items := encryptedReasoningTestInput(t, reqBody)
	reasoning := encryptedReasoningTestItem(t, items, 0)
	_, hasEncrypted := reasoning["encrypted_content"]
	require.False(t, hasEncrypted)
	require.Equal(t, "rs_6ad05224", encryptedReasoningTestField(t, reasoning, "id"))
	require.Equal(t, "completed", encryptedReasoningTestField(t, reasoning, "status"))
	require.Equal(t, "keep", encryptedReasoningTestSummaryText(t, reasoning))
}

// S2: drop the now-dangling id but keep the item as a placeholder.
func TestOpenAIEncryptedReasoningStageTwoDropsReasoningID(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
		{"type":"reasoning","id":"rs_6ad05224","status":"completed","summary":[{"type":"summary_text","text":"keep"}]},
		{"type":"message","content":[{"type":"input_text","text":"hi"}]}]}`)

	action, changed := applyOpenAIEncryptedReasoningStage(reqBody, openAIEncryptedReasoningStageDropReasoningID)
	require.True(t, changed)
	require.Equal(t, openAIEncryptedReasoningActionDropReasoningID, action)

	items := encryptedReasoningTestInput(t, reqBody)
	require.Len(t, items, 2, "the placeholder item must survive, unlike stage three")
	reasoning := encryptedReasoningTestItem(t, items, 0)
	_, hasID := reasoning["id"]
	require.False(t, hasID)
	require.Equal(t, "reasoning", encryptedReasoningTestField(t, reasoning, "type"))
	require.Equal(t, "completed", encryptedReasoningTestField(t, reasoning, "status"))
	require.Equal(t, "keep", encryptedReasoningTestSummaryText(t, reasoning))
}

// S2 also removes an item left with nothing but its type, mirroring
// sanitizeEncryptedReasoningInputItem's handling of a stripped encrypted_content.
func TestOpenAIEncryptedReasoningStageTwoDropsEmptyShell(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
		{"type":"reasoning","id":"rs_only_id"},
		{"type":"message","content":"hi"}]}`)

	_, changed := applyOpenAIEncryptedReasoningStage(reqBody, openAIEncryptedReasoningStageDropReasoningID)
	require.True(t, changed)

	items := encryptedReasoningTestInput(t, reqBody)
	require.Len(t, items, 1)
	require.Equal(t, "message", encryptedReasoningTestType(t, items, 0))
}

// S3: drop every reasoning item in full; unrelated items stay.
func TestOpenAIEncryptedReasoningStageThreeDropsWholeReasoningItem(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
		{"type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"shell"}]},
		{"type":"message","content":[{"type":"input_text","text":"hi"}]},
		{"type":"function_call_output","call_id":"call_1","output":"result"}]}`)

	action, changed := applyOpenAIEncryptedReasoningStage(reqBody, openAIEncryptedReasoningStageDropReasoningItem)
	require.True(t, changed)
	require.Equal(t, openAIEncryptedReasoningActionDropReasoningItem, action)

	items := encryptedReasoningTestInput(t, reqBody)
	require.Len(t, items, 2)
	require.Equal(t, "message", encryptedReasoningTestType(t, items, 0))
	require.Equal(t, "function_call_output", encryptedReasoningTestType(t, items, 1))
}

// S3 also removes "input" entirely once nothing is left.
func TestOpenAIEncryptedReasoningStageThreeRemovesEmptyInput(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"model":"gpt-5","input":[{"type":"reasoning","summary":[]}]}`)

	_, changed := applyOpenAIEncryptedReasoningStage(reqBody, openAIEncryptedReasoningStageDropReasoningItem)
	require.True(t, changed)
	_, hasInput := reqBody["input"]
	require.False(t, hasInput)
}

// The refactor of dropOpenAIEncryptedReasoningInputItems onto the shared walker
// must keep its default semantics: only reasoning items carrying
// encrypted_content are dropped, and the id-preserving trimmer is untouched.
func TestDropOpenAIEncryptedReasoningInputItemsKeepsDefaultSemantics(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"blob"},
		{"type":"reasoning","id":"rs_2","summary":[]},
		{"type":"compaction","id":"cmp_1","encrypted_content":"blob"},
		{"type":"message","content":"hi"}]}`)

	require.True(t, dropOpenAIEncryptedReasoningInputItems(reqBody))
	items := encryptedReasoningTestInput(t, reqBody)
	require.Len(t, items, 3, "only the encrypted reasoning item is dropped; compaction is trim's job, not drop's")
	require.Equal(t, "rs_2", encryptedReasoningTestField(t, encryptedReasoningTestItem(t, items, 0), "id"))
	require.Equal(t, "compaction", encryptedReasoningTestType(t, items, 1))
	require.Equal(t, "message", encryptedReasoningTestType(t, items, 2))
}

func TestDropOpenAIEncryptedReasoningInputItemsNoopWithoutEncryptedReasoning(t *testing.T) {
	for _, raw := range []string{
		`{"input":[{"type":"reasoning","id":"rs_2","summary":[]},{"type":"message","content":"hi"}]}`,
		`{"input":{"type":"message","content":"hi"}}`,
		`{"model":"gpt-5"}`,
		`{"input":"not-an-array"}`,
		`{}`,
	} {
		reqBody := decodeEncryptedReasoningTestBody(t, raw)
		require.False(t, dropOpenAIEncryptedReasoningInputItems(reqBody), "raw: %s", raw)
	}
}

// The walker must understand every encoding of `input` the gateway accepts.
func TestRewriteOpenAIInputItemsInputEncodings(t *testing.T) {
	t.Run("bare object input", func(t *testing.T) {
		reqBody := decodeEncryptedReasoningTestBody(t, `{"input":{"type":"reasoning","id":"rs_1","encrypted_content":"blob"}}`)
		require.True(t, trimOpenAIEncryptedReasoningItems(reqBody))
		inputItem, ok := reqBody["input"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "rs_1", encryptedReasoningTestField(t, inputItem, "id"))
	})

	t.Run("typed slice input", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []map[string]any{
				{"type": "reasoning", "id": "rs_1", "encrypted_content": "blob"},
				{"type": "message", "content": "hi"},
			},
		}
		require.True(t, trimOpenAIEncryptedReasoningItems(reqBody))
		items, ok := reqBody["input"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, items, 2)
		_, hasEncrypted := items[0]["encrypted_content"]
		require.False(t, hasEncrypted)
		require.Equal(t, "rs_1", encryptedReasoningTestField(t, items[0], "id"))
	})
}

// The ladder deepens one rung at a time, in order, and every rung runs exactly
// once per request.
func TestOpenAIEncryptedReasoningLadderEscalatesInOrderAndOnlyOnce(t *testing.T) {
	reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
		{"type":"reasoning","id":"rs_6ad05224","status":"completed","summary":[{"type":"summary_text","text":"keep"}],"encrypted_content":"blob"},
		{"type":"message","content":[{"type":"input_text","text":"hi"}]}]}`)
	ladder := &openAIEncryptedReasoningLadder{}

	stage, action, skipped := ladder.escalate(reqBody)
	require.Equal(t, openAIEncryptedReasoningStageDropEncryptedContent, stage)
	require.Equal(t, openAIEncryptedReasoningActionDropEncryptedContent, action)
	require.Empty(t, skipped)
	items := encryptedReasoningTestInput(t, reqBody)
	reasoning := encryptedReasoningTestItem(t, items, 0)
	_, hasEncrypted := reasoning["encrypted_content"]
	require.False(t, hasEncrypted)
	require.Equal(t, "rs_6ad05224", encryptedReasoningTestField(t, reasoning, "id"), "stage one must keep the id")

	stage, action, skipped = ladder.escalate(reqBody)
	require.Equal(t, openAIEncryptedReasoningStageDropReasoningID, stage)
	require.Equal(t, openAIEncryptedReasoningActionDropReasoningID, action)
	require.Empty(t, skipped)
	reasoning = encryptedReasoningTestItem(t, encryptedReasoningTestInput(t, reqBody), 0)
	_, hasID := reasoning["id"]
	require.False(t, hasID, "stage two must drop the dangling id")

	stage, action, skipped = ladder.escalate(reqBody)
	require.Equal(t, openAIEncryptedReasoningStageDropReasoningItem, stage)
	require.Equal(t, openAIEncryptedReasoningActionDropReasoningItem, action)
	require.Empty(t, skipped)
	items = encryptedReasoningTestInput(t, reqBody)
	require.Len(t, items, 1)
	require.Equal(t, "message", encryptedReasoningTestType(t, items, 0))

	stage, action, skipped = ladder.escalate(reqBody)
	require.Equal(t, openAIEncryptedReasoningStageNone, stage, "the ladder must be exhausted after three rungs")
	require.Empty(t, action)
	require.Empty(t, skipped)
	require.Equal(t, [openAIEncryptedReasoningStageMax + 1]int{0, 1, 1, 1}, ladder.attempts)
}

// A rung whose target items are absent is counted as attempted, reported as
// skipped, and must not stop the ladder from reaching a rung that can act.
func TestOpenAIEncryptedReasoningLadderSkipsRungsWithoutTargets(t *testing.T) {
	t.Run("no reasoning items at all", func(t *testing.T) {
		reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[{"type":"message","content":"hi"}]}`)
		ladder := &openAIEncryptedReasoningLadder{}

		stage, _, skipped := ladder.escalate(reqBody)
		require.Equal(t, openAIEncryptedReasoningStageNone, stage)
		require.Equal(t, []openAIEncryptedReasoningSkip{
			{Stage: openAIEncryptedReasoningStageDropEncryptedContent, Action: openAIEncryptedReasoningActionDropEncryptedContent},
			{Stage: openAIEncryptedReasoningStageDropReasoningID, Action: openAIEncryptedReasoningActionDropReasoningID},
			{Stage: openAIEncryptedReasoningStageDropReasoningItem, Action: openAIEncryptedReasoningActionDropReasoningItem},
		}, skipped)

		stage, _, skipped = ladder.escalate(reqBody)
		require.Equal(t, openAIEncryptedReasoningStageNone, stage)
		require.Empty(t, skipped, "an exhausted ladder must not retry")
	})

	t.Run("no encrypted_content leaves stage one without a target", func(t *testing.T) {
		reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
			{"type":"reasoning","id":"rs_1","status":"completed","summary":[{"type":"summary_text","text":"keep"}]},
			{"type":"message","content":"hi"}]}`)
		ladder := &openAIEncryptedReasoningLadder{}

		stage, action, skipped := ladder.escalate(reqBody)
		require.Equal(t, openAIEncryptedReasoningStageDropReasoningID, stage,
			"stage one is skipped and stage two is reached without an upstream retry in between")
		require.Equal(t, openAIEncryptedReasoningActionDropReasoningID, action)
		require.Equal(t, []openAIEncryptedReasoningSkip{
			{Stage: openAIEncryptedReasoningStageDropEncryptedContent, Action: openAIEncryptedReasoningActionDropEncryptedContent},
		}, skipped)
	})

	t.Run("id-less shell leaves stage two without a target", func(t *testing.T) {
		reqBody := decodeEncryptedReasoningTestBody(t, `{"input":[
			{"type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"keep"}]},
			{"type":"message","content":"hi"}]}`)
		ladder := &openAIEncryptedReasoningLadder{}

		stage, action, _ := ladder.escalate(reqBody)
		require.Equal(t, openAIEncryptedReasoningStageDropReasoningItem, stage)
		require.Equal(t, openAIEncryptedReasoningActionDropReasoningItem, action)
		items := encryptedReasoningTestInput(t, reqBody)
		require.Len(t, items, 1)
		require.Equal(t, "message", encryptedReasoningTestType(t, items, 0))
	})
}
