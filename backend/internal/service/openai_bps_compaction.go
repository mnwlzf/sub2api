package service

// BPS 压缩（阶段 5）：/responses/compact 与带 compaction_trigger 的普通
// Responses 请求都会向 BPS 发起一次真实摘要请求。
//
// 语义（见 openspec design.md D7）：
//   - 摘要请求关闭客户端工具：不收集 tools、不注入工具目录、上游 body 里没有
//     tools 字段；只追加一段摘要指令；
//   - 上游返回的真实文本摘要存 Redis（7 天闲置续期、跨实例），客户端只收到
//     bpscmp_v1_ 前缀的版本化不透明引用；
//   - 后续请求由服务端解析引用恢复摘要，引用字符串绝不出现在上游 body 里；
//   - 压缩记录保存 turn_id 与迭代基数，连续压缩保留最初轮次锚点；
//   - 摘要为空或上游改调工具时返回 bps_compaction_failed，绝不用不完整摘要
//     替换客户端历史。
import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	// openAIBPSCompactPrefix 是客户端可见的压缩引用版本前缀。
	openAIBPSCompactPrefix = "bpscmp_v1_"

	// OpenAIBPSErrorCompactionFailed 表示摘要请求没有产出可用的文本摘要。
	OpenAIBPSErrorCompactionFailed = "bps_compaction_failed"
)

// openAIBPSSummaryPrompt 是压缩请求追加给上游的摘要指令（真实摘要请求）。
const openAIBPSSummaryPrompt = `Summarize the conversation so a successor assistant can continue the user's task after earlier messages are discarded. Preserve the user's requests and constraints, decisions, important paths and code, completed work, actual tool results and errors, pending work and the immediate next step. Incorporate relevant earlier summaries. Return only the summary as plain text. Do not call tools.`

// openAIBPSCompactionCursor 记录客户端历史里最近一条可恢复的压缩引用。
// Index 为 -1 表示没有压缩引用；State 是 Redis 里取回的摘要与轮次锚点。
type openAIBPSCompactionCursor struct {
	Index int
	Ref   string
	State openAIBPSCompactState
}

// openAIBPSCompactPrefixMatches 报告引用是否带当前版本前缀。
func openAIBPSCompactPrefixMatches(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), openAIBPSCompactPrefix)
}

// openAIBPSInputHasCompactionTrigger 报告 input 里是否带服务端压缩信号。
func openAIBPSInputHasCompactionTrigger(input []any) bool {
	for _, raw := range input {
		if item, ok := raw.(map[string]any); ok && stringValue(item["type"]) == "compaction_trigger" {
			return true
		}
	}
	return false
}

// openAIBPSCompactRefFromBody 从客户端请求体里提取最近一条压缩引用。
//
// 只接受带版本前缀的引用：其它 provider 的压缩块无法恢复，交给 prepare
// 阶段按 400 拒绝。这里用于「无显式会话 ID 时由引用恢复原会话」。
func openAIBPSCompactRefFromBody(body []byte) string {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return ""
	}
	ref := ""
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "compaction", "compaction_summary":
			if candidate := strings.TrimSpace(item.Get("encrypted_content").String()); openAIBPSCompactPrefixMatches(candidate) {
				ref = candidate
			}
		}
	}
	return ref
}

// openAIBPSLoadCompactState 读取压缩摘要，并把存储错误映射成对外错误码。
func openAIBPSLoadCompactState(ctx context.Context, store *OpenAIBPSSessionStore, prepared *openAIBPSPreparedRequest, ref string) (openAIBPSCompactState, error) {
	if store == nil {
		return openAIBPSCompactState{}, openAIBPSStateUnavailable()
	}
	state, err := store.LoadCompactState(ctx, prepared.APIKeyID, prepared.SessionKey, ref)
	if err == nil {
		return state, nil
	}
	switch {
	case errors.Is(err, ErrOpenAIBPSCompactExpired):
		return openAIBPSCompactState{}, openAIBPSContextExpired("BPS compaction state has expired; start a new conversation")
	case errors.Is(err, ErrOpenAIBPSSessionStoreUnavailable):
		return openAIBPSCompactState{}, openAIBPSStateUnavailable()
	default:
		return openAIBPSCompactState{}, openAIBPSStateUnavailable()
	}
}

// openAIBPSOutputText 拼接响应里所有 message 的 output_text。
func openAIBPSOutputText(response map[string]any) string {
	output, _ := response["output"].([]any)
	parts := make([]string, 0, len(output))
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok || stringValue(item["type"]) != "message" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok || stringValue(part["type"]) != "output_text" {
				continue
			}
			parts = append(parts, stringValue(part["text"]))
		}
	}
	return strings.Join(parts, "\n")
}

// openAIBPSCompactResponse 把摘要请求的终态响应转换成客户端压缩响应：
// 保存真实摘要与轮次锚点，只回写版本化不透明引用。
func openAIBPSCompactResponse(ctx context.Context, store *OpenAIBPSSessionStore, prepared *openAIBPSPreparedRequest, response map[string]any, output []any) (map[string]any, error) {
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "function_call", "custom_tool_call":
			// 摘要请求已经关闭客户端工具；上游仍然要求工具说明它没有产出摘要。
			return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorCompactionFailed,
				"BPS requested a tool instead of returning a conversation summary")
		}
	}
	summary := strings.TrimSpace(openAIBPSOutputText(response))
	if summary == "" {
		return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorCompactionFailed,
			"BPS did not return a conversation summary")
	}
	if store == nil {
		return nil, openAIBPSStateUnavailable()
	}
	ref := openAIBPSCompactPrefix + uuid.NewString()
	state := openAIBPSCompactState{
		Summary:        summary,
		TurnID:         prepared.TurnID,
		AgentIteration: prepared.AgentIteration,
		Version:        1,
		CreatedAt:      time.Now().Unix(),
	}
	if err := store.SaveCompactState(ctx, prepared.APIKeyID, prepared.SessionKey, ref, state); err != nil {
		return nil, openAIBPSStateUnavailable()
	}
	if err := store.SaveCompactReference(ctx, prepared.APIKeyID, ref, prepared.SessionKey); err != nil {
		return nil, openAIBPSStateUnavailable()
	}
	response["output"] = []any{map[string]any{
		"type":              "compaction",
		"id":                "cmp_" + uuid.NewString(),
		"status":            "completed",
		"encrypted_content": ref,
	}}
	delete(response, "output_text")
	response["model"] = prepared.ClientModel
	if prepared.CompactEndpoint {
		response["object"] = "response.compaction"
	}
	return response, nil
}
