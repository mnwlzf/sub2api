package service

import "strings"

// Graded recovery ladder for upstream 400s that blame a replayed encrypted
// reasoning block.
//
// The upstream binds a reasoning item's `encrypted_content` to the caller that
// minted it — in practice the exit IP, since replaying the same blob from the same
// exit succeeds even on another account, while replaying it from a different exit
// fails. Because we rotate the WARP exit on 429 and fail over between accounts,
// every rotation invalidates the blobs already sitting in the client's replayed
// history, and the upstream answers 400 in three related shapes:
//
//   - code=invalid_encrypted_content
//   - code=thinking_signature_invalid + "The encrypted content ... could not be
//     verified ... could not be decrypted or parsed"
//   - code=null + "reasoning `encrypted_content` was not issued to this caller"
//
// Stripping only `encrypted_content` is not enough. The surviving reasoning `id`
// then references an item whose content the upstream can no longer resolve, so the
// next attempt fails with a second 400: "Referenced reasoning item 'rs_...' was not
// found or has expired." Verified against the live upstream by replaying one freshly
// minted blob from a different exit:
//
//	original body (blob + id)             -> 400 "was not issued to this caller"
//	drop encrypted_content, keep id       -> 400 "was not found or has expired"
//	drop encrypted_content and id         -> 200
//
// The recovery is therefore a ladder that deepens one rung at a time and stops as
// soon as the upstream accepts:
//
//	S1 drop `encrypted_content`, keep id + summary  -> fixes the caller-mismatch shape
//	S2 additionally drop the now-dangling `id`      -> fixes the not-found shape
//	S3 drop the whole reasoning item                -> last resort
//
// Each rung runs at most once per request, so the ladder performs at most three
// retries and can never loop.
type openAIEncryptedReasoningStage int

const (
	openAIEncryptedReasoningStageNone openAIEncryptedReasoningStage = iota
	openAIEncryptedReasoningStageDropEncryptedContent
	openAIEncryptedReasoningStageDropReasoningID
	openAIEncryptedReasoningStageDropReasoningItem
)

const openAIEncryptedReasoningStageMax = int(openAIEncryptedReasoningStageDropReasoningItem)

// Action labels are part of the observable log line so the depth actually reached
// can be read straight out of production logs.
const (
	openAIEncryptedReasoningActionDropEncryptedContent = "drop_encrypted_content"
	openAIEncryptedReasoningActionDropReasoningID      = "drop_reasoning_id"
	openAIEncryptedReasoningActionDropReasoningItem    = "drop_reasoning_item"
)

// openAIEncryptedReasoningLadder counts how many times each rung of the recovery
// ladder already ran during one Forward call. A rung is eligible only while its
// counter is zero, which is what bounds the ladder to a single pass.
type openAIEncryptedReasoningLadder struct {
	attempts [openAIEncryptedReasoningStageMax + 1]int
}

// openAIEncryptedReasoningSkip records a rung that was reached but could not act
// because its target items are absent from the request.
type openAIEncryptedReasoningSkip struct {
	Stage  openAIEncryptedReasoningStage
	Action string
}

// escalate walks the ladder from the shallowest rung that has not run yet and
// applies the first rung that actually changes the body. Rungs whose target items
// are absent are counted as attempted and reported through skipped (so the caller
// can log the guard hit) but never trigger a retry on their own. Rungs are applied
// in order because each one assumes the shallower rungs already ran.
//
// It returns stage == openAIEncryptedReasoningStageNone when no rung could change
// the body, i.e. the ladder is either exhausted or has no target at all.
func (l *openAIEncryptedReasoningLadder) escalate(reqBody map[string]any) (openAIEncryptedReasoningStage, string, []openAIEncryptedReasoningSkip) {
	var skipped []openAIEncryptedReasoningSkip
	for stage := openAIEncryptedReasoningStage(1); int(stage) <= openAIEncryptedReasoningStageMax; stage++ {
		if l.attempts[stage] > 0 {
			continue
		}
		l.attempts[stage]++
		action, changed := applyOpenAIEncryptedReasoningStage(reqBody, stage)
		if changed {
			return stage, action, skipped
		}
		skipped = append(skipped, openAIEncryptedReasoningSkip{Stage: stage, Action: action})
	}
	return openAIEncryptedReasoningStageNone, "", skipped
}

// applyOpenAIEncryptedReasoningStage applies one rung in place and reports whether
// it changed the body. The rungs are cumulative: S2 assumes S1 already stripped
// every encrypted_content, and S3 assumes S1 and S2 already ran.
func applyOpenAIEncryptedReasoningStage(reqBody map[string]any, stage openAIEncryptedReasoningStage) (action string, changed bool) {
	switch stage {
	case openAIEncryptedReasoningStageDropEncryptedContent:
		// S1 reuses the shared trimmer unchanged: strip encrypted_content (and null
		// content) while keeping the reasoning item's id and summary.
		return openAIEncryptedReasoningActionDropEncryptedContent, trimOpenAIEncryptedReasoningItems(reqBody)
	case openAIEncryptedReasoningStageDropReasoningID:
		return openAIEncryptedReasoningActionDropReasoningID, dropOpenAIReasoningItemIDs(reqBody)
	case openAIEncryptedReasoningStageDropReasoningItem:
		return openAIEncryptedReasoningActionDropReasoningItem, dropOpenAIReasoningInputItems(reqBody)
	default:
		return "", false
	}
}

// dropOpenAIReasoningItemIDs removes the `id` of reasoning input items while
// keeping the item itself (type, status, summary) as a placeholder, exactly the
// transformation measured as a 200 against the live upstream. An item left with
// nothing but its type is dropped, mirroring sanitizeEncryptedReasoningInputItem's
// behaviour for a stripped encrypted_content.
//
// This is deliberately a separate function rather than a change to
// sanitizeEncryptedReasoningInputItem: that helper is shared by the multi-provider
// callers whose default semantics must keep the id.
func dropOpenAIReasoningItemIDs(reqBody map[string]any) bool {
	return rewriteOpenAIInputItems(reqBody, func(item any) (next any, changed bool, keep bool) {
		inputItem, ok := item.(map[string]any)
		if !ok {
			return item, false, true
		}
		if itemType, _ := inputItem["type"].(string); strings.TrimSpace(itemType) != "reasoning" {
			return item, false, true
		}
		if _, has := inputItem["id"]; !has {
			return item, false, true
		}
		delete(inputItem, "id")
		if len(inputItem) == 1 {
			return nil, true, false
		}
		return inputItem, true, true
	})
}

// dropOpenAIReasoningInputItems removes every reasoning input item in full,
// including its coupled id and summary. Stage 1 already stripped every
// encrypted_content and stage 2 every id, so by the time stage 3 runs the remaining
// reasoning items are exactly the unresolvable shells that
// dropOpenAIEncryptedReasoningInputItems would have matched before they were
// hollowed out — the two predicates describe the same set at this point. It shares
// that function's walker while dropping on the item type rather than on the
// encrypted_content marker, which no longer exists.
func dropOpenAIReasoningInputItems(reqBody map[string]any) bool {
	return rewriteOpenAIInputItems(reqBody, func(item any) (next any, changed bool, keep bool) {
		if isOpenAIReasoningInputItem(item) {
			return nil, true, false
		}
		return item, false, true
	})
}

// isOpenAIReasoningInputItem reports whether item is a Responses reasoning item.
func isOpenAIReasoningInputItem(item any) bool {
	inputItem, ok := item.(map[string]any)
	if !ok {
		return false
	}
	itemType, _ := inputItem["type"].(string)
	return strings.TrimSpace(itemType) == "reasoning"
}

// rewriteOpenAIInputItems walks a request's `input` item list and lets rewrite
// mutate an item in place, replace it, or drop it (keep == false), reporting
// whether anything changed. It is the shared walker behind the encrypted-reasoning
// recovery helpers so they all agree on how `input` may be encoded ([]any,
// []map[string]any, or a bare object) — mirroring trimOpenAIEncryptedReasoningItems,
// which it does not replace.
func rewriteOpenAIInputItems(reqBody map[string]any, rewrite func(item any) (next any, changed bool, keep bool)) bool {
	if len(reqBody) == 0 {
		return false
	}
	inputValue, has := reqBody["input"]
	if !has {
		return false
	}
	switch input := inputValue.(type) {
	case []any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			nextItem, itemChanged, keep := rewrite(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				continue
			}
			filtered = append(filtered, nextItem)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return true
		}
		reqBody["input"] = filtered
		return true
	case []map[string]any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			nextItem, itemChanged, keep := rewrite(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				continue
			}
			nextMap, ok := nextItem.(map[string]any)
			if !ok {
				filtered = append(filtered, item)
				continue
			}
			filtered = append(filtered, nextMap)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return true
		}
		reqBody["input"] = filtered
		return true
	case map[string]any:
		nextItem, changed, keep := rewrite(input)
		if !changed {
			return false
		}
		if !keep {
			delete(reqBody, "input")
			return true
		}
		nextMap, ok := nextItem.(map[string]any)
		if !ok {
			return false
		}
		reqBody["input"] = nextMap
		return true
	default:
		return false
	}
}
