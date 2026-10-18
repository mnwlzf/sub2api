package service

import (
	"fmt"
	"strings"
)

// OpenAI Responses 的"上游私有回放状态"剥离。
//
// 背景：Responses 的 reasoning item（及其 encrypted_content）和各类 item id
// 都绑定创建它的上游资源。客户端在 store:false 下每轮把完整历史原样回放，
// 一旦上游是"同一账号背后多个资源轮转"的池化中转（典型：Azure OpenAI 资源池），
// 相邻两轮落到不同资源就会 400：
//
//	The requested item was created under a different Azure OpenAI resource.
//	Item 'fc_...' of type 'function_call' was provided without its required
//	'reasoning' item: 'rs_...'.
//
// 客户端无法预知上游是池化实现，因此由网关按账号显式开关剥离：把历史里绑定
// 资源的 reasoning 项、item_reference、compaction 项整条删除，并删掉其余 item
// 的 id / encrypted_content，让请求退化为"纯内容重放"——这正是客户端跨模型/
// 跨 provider 回放时的形状，任何资源都能接受。
//
// 代价：该账号的请求不再复用跨轮推理链（模型只看到消息与工具输出）。因此只对
// 池化上游账号开启；默认关闭。

// ReplayStripStats 记录一次剥离处理的各类计数，用于日志观测。
type ReplayStripStats struct {
	DroppedReasoning  int
	DroppedReferences int
	DroppedCompaction int
	StrippedItemIDs   int
}

// Changed 报告是否有任何内容被剥离。
func (s ReplayStripStats) Changed() bool {
	return s.DroppedReasoning > 0 ||
		s.DroppedReferences > 0 ||
		s.DroppedCompaction > 0 ||
		s.StrippedItemIDs > 0
}

// StripOpenAIResponsesForeignReplayState 剥离 Responses 请求 input 中绑定上游
// 资源的回放状态。仅处理 input 为数组的请求；其余请求原样返回。
//
// 只应在 store:false（或 /responses/compact）请求上调用：有状态请求依赖 item id
// 引用服务端对象，剥离会破坏语义。
func StripOpenAIResponsesForeignReplayState(body []byte) ([]byte, bool, ReplayStripStats, error) {
	var stats ReplayStripStats
	if len(body) == 0 {
		return body, false, stats, nil
	}
	input := parseRawJSONView(body).Get("input")
	if !input.IsArray() {
		return body, false, stats, nil
	}
	var reqBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &reqBody); err != nil {
		return body, false, ReplayStripStats{}, fmt.Errorf("decode responses replay strip body: %w", err)
	}
	items, ok := reqBody["input"].([]any)
	if !ok {
		return body, false, stats, nil
	}
	kept := make([]any, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			kept = append(kept, rawItem)
			continue
		}
		itemType, _ := item["type"].(string)
		switch strings.TrimSpace(itemType) {
		case "reasoning":
			stats.DroppedReasoning++
			continue
		case "item_reference":
			stats.DroppedReferences++
			continue
		case "compaction", "compaction_summary":
			stats.DroppedCompaction++
			continue
		}
		if _, has := item["id"]; has {
			delete(item, "id")
			stats.StrippedItemIDs++
		}
		// encrypted_content 只出现在 reasoning/compaction 之外的非标准项上时
		// 一并删除，避免把绑定资源的密文透传给另一个资源。
		delete(item, "encrypted_content")
		kept = append(kept, item)
	}
	if !stats.Changed() {
		return body, false, stats, nil
	}
	reqBody["input"] = kept
	out, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false, ReplayStripStats{}, fmt.Errorf("encode responses replay strip body: %w", err)
	}
	return out, true, stats, nil
}
