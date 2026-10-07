package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 协议适配器：把一份已发布的提示词正文投影到某个出站协议的最终请求体。
//
// 这里是纯函数，不读数据库、不碰 gin context、不发请求。所有“注入是否落在正确
// 字段、是否只注入一次、是否保留客户端原有内容”的验收都可以只针对这些函数做
// 契约测试，再在真实出站路径上用 httpUpstreamRecorder 断言最终 body。
//
// 设计约束（见 docs/plans/2026-10-07-text-prompt-management-design.md 第 7 节）：
//   - 不替换、不合并客户端已有的 system / developer / instructions。
//   - 不改变其它字段（tools、tool_calls、多模态 content、stream、reasoning 等）。
//   - 只新增一个字段/消息，重复调用不会累积（幂等性由“每次从原始 body 派生”保证）。

// ApplyPromptToChatBody 在 Chat Completions 请求体中插入一条独立的 system 消息。
//
// 插入位置：所有前导的 system / developer 消息之后、第一条普通对话消息之前。
// 这样既保留客户端原有系统指令的相对顺序，又让服务端追加的提示词紧跟其后。
// 原有消息的原始字节被完整保留（直接复用 gjson 的 Raw），因此多模态 content
// 数组、tool_calls、tool 角色等结构不会因为重新序列化而改变。
func ApplyPromptToChatBody(body []byte, prompt string) ([]byte, error) {
	if prompt == "" {
		return body, nil
	}
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("chat request body is not valid JSON")
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil, fmt.Errorf("chat request body has no messages array")
	}

	items := messages.Array()
	insertAt := 0
	for _, item := range items {
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role == "system" || role == "developer" {
			insertAt++
			continue
		}
		break
	}

	encoded, err := json.Marshal(map[string]string{"role": "system", "content": prompt})
	if err != nil {
		return nil, fmt.Errorf("encode injected system message: %w", err)
	}
	raw := make([]json.RawMessage, 0, len(items)+1)
	for i, item := range items {
		if i == insertAt {
			raw = append(raw, json.RawMessage(encoded))
		}
		raw = append(raw, json.RawMessage(item.Raw))
	}
	if insertAt >= len(items) {
		raw = append(raw, json.RawMessage(encoded))
	}
	arrayBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode injected messages array: %w", err)
	}
	next, err := sjson.SetRawBytes(body, "messages", arrayBytes)
	if err != nil {
		return nil, fmt.Errorf("apply prompt to chat messages: %w", err)
	}
	return next, nil
}

// ApplyPromptToResponsesBody 把提示词追加到 Responses 请求体的顶层 instructions。
//
// 语义：原有字符串 instructions 在前，服务端提示词在后，用空行分隔；原字段为空
// 或不存在时直接使用提示词。instructions 存在但不是字符串时返回错误，而不是
// 强行 stringify —— 那会静默破坏客户端结构。
func ApplyPromptToResponsesBody(body []byte, prompt string) ([]byte, error) {
	if prompt == "" {
		return body, nil
	}
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("responses request body is not valid JSON")
	}
	existing := gjson.GetBytes(body, "instructions")
	if existing.Exists() && existing.Type != gjson.String {
		return nil, fmt.Errorf("responses instructions must be a string, got %s", existing.Type.String())
	}

	combined := prompt
	if prior := existing.String(); strings.TrimSpace(prior) != "" {
		combined = prior + "\n\n" + prompt
	}
	next, err := sjson.SetBytes(body, "instructions", combined)
	if err != nil {
		return nil, fmt.Errorf("apply prompt to responses instructions: %w", err)
	}
	return next, nil
}

// ApplyPromptForProfile 按 profile 选择正确的协议投影。
func ApplyPromptForProfile(profile string, body []byte, prompt string) ([]byte, error) {
	switch profile {
	case PromptProfileChatHTTP, PromptProfileResponsesToChat:
		return ApplyPromptToChatBody(body, prompt)
	case PromptProfileResponsesHTTP, PromptProfileChatToResponses:
		return ApplyPromptToResponsesBody(body, prompt)
	default:
		return nil, fmt.Errorf("unsupported prompt profile: %s", profile)
	}
}

// PromptBodyTargetsImageGeneration 判断请求体是否以原生图片生成工具为主目标。
//
// 这类请求不属于“纯文本对话”注入范围：即使它们同时带有文本字段，注入系统提示词
// 也可能改变图片工具的行为。命中时按设计记录 skipped_non_text_task 并跳过注入。
func PromptBodyTargetsImageGeneration(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, item := range tools.Array() {
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "image_generation") {
				return true
			}
		}
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if choice.IsObject() && strings.EqualFold(strings.TrimSpace(choice.Get("type").String()), "image_generation") {
		return true
	}
	return false
}
