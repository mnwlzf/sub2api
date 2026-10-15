package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollectOpenAIEncryptedContentDigestsRaw(t *testing.T) {
	t.Parallel()

	digests := collectOpenAIEncryptedContentDigestsRaw([]byte(`{"input":[
		{"type":"reasoning","encrypted_content":"cipher-a"},
		{"type":"compaction","encrypted_content":"cipher-b"},
		{"type":"input_text","text":"hello"},
		{"type":"reasoning"},
		{"type":"message","encrypted_content":"cipher-alien"}
	]}`))
	// 剥离端处理不了的类型（message）不收摘要，否则该摘要永远命中却剥不掉。
	require.Equal(t, []string{
		openAIEncryptedContentDigest("cipher-a"),
		openAIEncryptedContentDigest("cipher-b"),
	}, digests)

	require.Nil(t, collectOpenAIEncryptedContentDigestsRaw([]byte(`{"model":"gpt-5"}`)))
}

func TestStripOpenAIInvalidEncryptedContentItems(t *testing.T) {
	t.Parallel()

	// dropDanglingID=false：非 opencode 链路，行为必须与原来逐字一致（id 保留）。
	const dropDanglingID = false
	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("only_strips_matching_items", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "stale-cipher", "summary": []any{}},
				map[string]any{"type": "reasoning", "id": "rs_2", "encrypted_content": "fresh-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 3)
		first, ok := input[0].(map[string]any)
		require.True(t, ok)
		_, hasEncrypted := first["encrypted_content"]
		require.False(t, hasEncrypted, "命中项应剥离 encrypted_content")
		require.Equal(t, "rs_1", first["id"], "reasoning 骨架应保留")
		second, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "fresh-cipher", second["encrypted_content"], "新密文不得误删")
	})

	t.Run("compaction_removed_entirely", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "compaction", "encrypted_content": "stale-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		survivor, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "input_text", survivor["type"])
	})

	t.Run("no_hit_no_change", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "encrypted_content": "fresh-cipher"},
			},
		}
		require.Zero(t, stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID))
	})
}

func TestStripOpenAIInvalidEncryptedContentFromReplayItems(t *testing.T) {
	t.Parallel()

	// dropDanglingID=false：非 opencode 链路，id 保留。
	const dropDanglingID = false
	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("rewrites_hit_items_and_shares_the_rest", func(t *testing.T) {
		t.Parallel()
		original := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"}`)
		clean := json.RawMessage(`{"type":"input_text","text":"hello"}`)
		compaction := json.RawMessage(`{"type":"compaction","encrypted_content":"stale-cipher"}`)
		items := []json.RawMessage{original, clean, compaction}

		next, stripped := stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid, dropDanglingID)
		require.Equal(t, 2, stripped)
		require.Len(t, next, 2, "compaction 命中项应整项删除")
		var rewritten map[string]any
		require.NoError(t, json.Unmarshal(next[0], &rewritten))
		require.Equal(t, "rs_1", rewritten["id"])
		_, hasEncrypted := rewritten["encrypted_content"]
		require.False(t, hasEncrypted)
		// 未命中项正文共享；原命中项正文不被修改（整体替换语义）。
		require.Same(t, &clean[0], &next[1][0])
		require.Contains(t, string(original), "stale-cipher")
	})

	t.Run("miss_returns_original_header", func(t *testing.T) {
		t.Parallel()
		items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"fresh-cipher"}`)}
		next, stripped := stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid, dropDanglingID)
		require.Zero(t, stripped)
		require.Same(t, &items[0][0], &next[0][0])
	})
}

func TestStripOpenAIInvalidEncryptedContentRaw(t *testing.T) {
	t.Parallel()

	// dropDanglingID=false：非 opencode 链路，id 保留。
	const dropDanglingID = false
	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}
	payload := []byte(`{"model":"gpt-5","input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"},
		{"type":"reasoning","id":"rs_2","encrypted_content":"fresh-cipher"},
		{"type":"input_text","text":"hello"}
	],"stream":true}`)

	stripped, count, err := stripOpenAIInvalidEncryptedContentRaw(payload, invalid, dropDanglingID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stripped, &decoded))
	require.Equal(t, "gpt-5", decoded["model"])
	input, ok := decoded["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 3)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasEncrypted := first["encrypted_content"]
	require.False(t, hasEncrypted)
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fresh-cipher", second["encrypted_content"])

	t.Run("miss_fast_path_returns_original", func(t *testing.T) {
		t.Parallel()
		cleanPayload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"fresh-cipher"}]}`)
		unchanged, count, err := stripOpenAIInvalidEncryptedContentRaw(cleanPayload, invalid, dropDanglingID)
		require.NoError(t, err)
		require.Zero(t, count)
		require.Same(t, &cleanPayload[0], &unchanged[0], "未命中时应原样返回，不重编码")
	})
}

func TestOpenAIWSStateStoreInvalidEncryptedContentLineage(t *testing.T) {
	t.Parallel()

	t.Run("mark_get_roundtrip_with_merge", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		require.False(t, store.HasAnySessionInvalidEncryptedContent())
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-a"))

		store.MarkSessionInvalidEncryptedContent(1, "session-a", []string{"d1", "d2"}, time.Minute)
		store.MarkSessionInvalidEncryptedContent(1, "session-a", []string{"d2", "d3"}, time.Minute)
		require.True(t, store.HasAnySessionInvalidEncryptedContent())

		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-a")
		require.Len(t, digests, 3)
		for _, digest := range []string{"d1", "d2", "d3"} {
			require.Contains(t, digests, digest)
		}
		// 组隔离：另一组同名会话不可见。
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(2, "session-a"))
		// 返回值是拷贝，调用方修改不得影响存储。
		digests["d4"] = struct{}{}
		require.Len(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-a"), 3)
	})

	t.Run("expired_binding_not_returned", func(t *testing.T) {
		t.Parallel()
		raw := NewOpenAIWSStateStore(nil)
		raw.MarkSessionInvalidEncryptedContent(1, "session-b", []string{"d1"}, time.Minute)
		store, ok := raw.(*defaultOpenAIWSStateStore)
		require.True(t, ok)
		store.sessionInvalidEncryptedMu.Lock()
		binding := store.sessionInvalidEncrypted["1:session-b"]
		binding.expiresAt = time.Now().Add(-time.Second)
		store.sessionInvalidEncrypted["1:session-b"] = binding
		store.sessionInvalidEncryptedMu.Unlock()
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-b"))
	})

	t.Run("per_session_capacity_evicts_oldest_first", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		oversized := make([]string, 0, openAIWSInvalidEncryptedDigestsPerSession+10)
		for i := range openAIWSInvalidEncryptedDigestsPerSession + 10 {
			oversized = append(oversized, openAIEncryptedContentDigest(fmt.Sprintf("cipher-%d", i)))
		}
		store.MarkSessionInvalidEncryptedContent(1, "session-c", oversized, time.Minute)
		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-c")
		require.Len(t, digests, openAIWSInvalidEncryptedDigestsPerSession, "size 不得超过 cap")
		// 最新的 10 条（当前 body 的失效项）一定在集合内；最旧的 10 条被淘汰。
		for _, digest := range oversized[len(oversized)-10:] {
			require.Contains(t, digests, digest, "刚被拒绝的摘要一定记得上")
		}
		for _, digest := range oversized[:10] {
			require.NotContains(t, digests, digest, "最旧摘要应被 FIFO 淘汰")
		}
	})

	t.Run("fifo_eviction_across_marks_and_remark_is_stable", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		fill := make([]string, 0, openAIWSInvalidEncryptedDigestsPerSession)
		for i := range openAIWSInvalidEncryptedDigestsPerSession {
			fill = append(fill, openAIEncryptedContentDigest(fmt.Sprintf("fill-%d", i)))
		}
		store.MarkSessionInvalidEncryptedContent(1, "session-fifo", fill, time.Minute)
		// 跨次新增：只淘汰最旧的一条，其余保留。
		store.MarkSessionInvalidEncryptedContent(1, "session-fifo", []string{"brand-new"}, time.Minute)
		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-fifo")
		require.Len(t, digests, openAIWSInvalidEncryptedDigestsPerSession)
		require.Contains(t, digests, "brand-new")
		require.NotContains(t, digests, fill[0])
		require.Contains(t, digests, fill[1])
		// 重复标记已存在摘要：不占新坑、不触发淘汰、不膨胀。
		store.MarkSessionInvalidEncryptedContent(1, "session-fifo", []string{fill[1], "brand-new", "  ", ""}, time.Minute)
		again := store.GetSessionInvalidEncryptedContentDigests(1, "session-fifo")
		require.Len(t, again, openAIWSInvalidEncryptedDigestsPerSession)
		require.Contains(t, again, fill[1])
		require.Contains(t, again, "brand-new")
	})

	t.Run("remark_refreshes_ttl_sliding_window", func(t *testing.T) {
		t.Parallel()
		raw := NewOpenAIWSStateStore(nil)
		raw.MarkSessionInvalidEncryptedContent(1, "session-ttl", []string{"d1"}, time.Minute)
		store, ok := raw.(*defaultOpenAIWSStateStore)
		require.True(t, ok)
		store.sessionInvalidEncryptedMu.RLock()
		firstExpiry := store.sessionInvalidEncrypted["1:session-ttl"].expiresAt
		store.sessionInvalidEncryptedMu.RUnlock()
		// 再次标记（带更长 TTL）：全集合滑动续期，活跃会话的 lineage 常驻。
		raw.MarkSessionInvalidEncryptedContent(1, "session-ttl", []string{"d2"}, time.Hour)
		store.sessionInvalidEncryptedMu.RLock()
		secondExpiry := store.sessionInvalidEncrypted["1:session-ttl"].expiresAt
		store.sessionInvalidEncryptedMu.RUnlock()
		require.True(t, secondExpiry.After(firstExpiry), "TTL 应滑动续期")
		require.Len(t, raw.GetSessionInvalidEncryptedContentDigests(1, "session-ttl"), 2)
	})
}

// FIFO 淘汰必须可观测：沿用"锁外记日志"惯例，key 与已删除的
// capacity_overflow 不同（淘汰≠丢弃，不再有摘要记不上）。
// 非 parallel：与其它 captureStructuredLog 用例一致，避免重初始化全局 logger。
func TestOpenAIWSStateStoreInvalidEncryptedContentFIFOEvictLogs(t *testing.T) {
	sink, release := captureStructuredLog(t)
	defer release()

	store := NewOpenAIWSStateStore(nil)
	fill := make([]string, 0, openAIWSInvalidEncryptedDigestsPerSession+3)
	for i := range openAIWSInvalidEncryptedDigestsPerSession + 3 {
		fill = append(fill, openAIEncryptedContentDigest(fmt.Sprintf("evict-%d", i)))
	}
	store.MarkSessionInvalidEncryptedContent(1, "session-evict", fill, time.Minute)
	require.True(t, sink.ContainsMessage("invalid_encrypted_lineage_fifo_evict evicted_digests=3"),
		"淘汰 3 条旧摘要必须落日志")
	require.False(t, sink.ContainsMessage("invalid_encrypted_lineage_capacity_overflow"),
		"旧的丢弃语义已删除，不得再出现 overflow 日志")
}

// dropDanglingID=true（opencode_go 链路）：digest 命中的 reasoning 项同时删
// encrypted_content 和 id（S2 形状），status/summary 保留。
func TestStripOpenAIInvalidEncryptedContentItemsDropDanglingID(t *testing.T) {
	t.Parallel()

	const dropDanglingID = true
	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("hit_reasoning_loses_blob_and_id_but_keeps_status_summary", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "id": "rs_1", "status": "completed", "summary": []any{"s"}, "encrypted_content": "stale-cipher"},
				map[string]any{"type": "reasoning", "id": "rs_2", "encrypted_content": "fresh-cipher"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 2)
		first, ok := input[0].(map[string]any)
		require.True(t, ok)
		_, hasEncrypted := first["encrypted_content"]
		require.False(t, hasEncrypted, "命中项应剥离 encrypted_content")
		_, hasID := first["id"]
		require.False(t, hasID, "opencode_go 链路应连带删悬空 id")
		require.Equal(t, "completed", first["status"])
		require.Equal(t, []any{"s"}, first["summary"])
		second, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "fresh-cipher", second["encrypted_content"], "新密文不得误删")
		require.Equal(t, "rs_2", second["id"])
	})

	t.Run("bare_reasoning_shell_dropped_entirely", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "stale-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1, "只剩 type 的空壳应整项丢弃")
		survivor, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "input_text", survivor["type"])
	})

	t.Run("compaction_still_removed_entirely", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "compaction", "id": "cmp_1", "encrypted_content": "stale-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
	})

	t.Run("equals_sequential_s1_then_s2", func(t *testing.T) {
		t.Parallel()
		build := func() map[string]any {
			return map[string]any{
				"input": []any{
					map[string]any{"type": "reasoning", "id": "rs_1", "status": "completed", "summary": []any{"s"}, "encrypted_content": "stale-cipher"},
					map[string]any{"type": "message", "content": "hi"},
				},
			}
		}
		viaLineage := build()
		require.Equal(t, 1, stripOpenAIInvalidEncryptedContentItems(viaLineage, invalid, true))
		viaLadder := build()
		require.True(t, trimOpenAIEncryptedReasoningItems(viaLadder))
		require.True(t, dropOpenAIReasoningItemIDs(viaLadder))
		require.Equal(t, viaLadder, viaLineage, "lineage S2 改写必须等价于阶梯 S1→S2 的依次效果")
	})

	t.Run("non_reasoning_types_untouched", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "message", "content": "hi"},
				map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			},
		}
		require.Zero(t, stripOpenAIInvalidEncryptedContentItems(reqBody, invalid, dropDanglingID))
	})
}

func TestStripOpenAIInvalidEncryptedContentFromReplayItemsDropDanglingID(t *testing.T) {
	t.Parallel()

	const dropDanglingID = true
	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("hit_item_rewritten_to_s2_shape", func(t *testing.T) {
		t.Parallel()
		original := json.RawMessage(`{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"encrypted_content":"stale-cipher"}`)
		clean := json.RawMessage(`{"type":"input_text","text":"hello"}`)
		items := []json.RawMessage{original, clean}

		next, stripped := stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		require.Len(t, next, 2)
		var rewritten map[string]any
		require.NoError(t, json.Unmarshal(next[0], &rewritten))
		_, hasEncrypted := rewritten["encrypted_content"]
		require.False(t, hasEncrypted)
		_, hasID := rewritten["id"]
		require.False(t, hasID, "replay 路径命中项也应删悬空 id")
		require.Equal(t, "completed", rewritten["status"])
		require.Same(t, &clean[0], &next[1][0], "未命中项正文共享")
		require.Contains(t, string(original), "stale-cipher", "原正文不被修改")
	})

	t.Run("bare_shell_dropped", func(t *testing.T) {
		t.Parallel()
		items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"}`)}
		next, stripped := stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid, dropDanglingID)
		require.Equal(t, 1, stripped)
		require.Empty(t, next)
	})
}

func TestStripOpenAIInvalidEncryptedContentRawDropDanglingID(t *testing.T) {
	t.Parallel()

	const dropDanglingID = true
	invalid := map[string]struct{}{
		openAIEncryptedContentDigest("stale-cipher"): {},
	}
	payload := []byte(`{"model":"gpt-5","input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher","summary":[]},
		{"type":"reasoning","id":"rs_2","encrypted_content":"fresh-cipher"}
	]}`)

	stripped, count, err := stripOpenAIInvalidEncryptedContentRaw(payload, invalid, dropDanglingID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stripped, &decoded))
	input, ok := decoded["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasEncrypted := first["encrypted_content"]
	require.False(t, hasEncrypted)
	_, hasID := first["id"]
	require.False(t, hasID)
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fresh-cipher", second["encrypted_content"])
	require.Equal(t, "rs_2", second["id"])
}
