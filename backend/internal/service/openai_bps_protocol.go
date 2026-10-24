package service

// BPS 工具隧道协议层：工具目录、请求准备、上游工具调用 → 客户端工具调用
// 转换、客户端工具结果回放、响应终态校验。
//
// 参考实现的语义（见 docs/openai-bps-sources.md 冻结的提交）在这里被保持：
//   - 上游只看到 developer/instructions 里的协议说明 + run_officejs 载体，
//     客户端的 tools 声明不会直接发给上游；
//   - 一次响应最多交付一个客户端工具调用（BPS 串行工具）；
//   - 只有本次请求声明的工具可以输出；
//   - function 参数按声明 JSON Schema 校验，custom 输入是客户端原文；
//   - 回放时从 Redis 恢复上游原始 item，客户端工具结果转换成稳定 fc_ ID 的
//     function_call_output；加密 reasoning 重建为「类型 + 空 summary + 原密文」。
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// BPS 工具隧道错误码（响应体为 OpenAI 兼容错误信封）。
const (
	// OpenAIBPSErrorInvalidToolCall 表示上游工具调用信封无法解析或参数非法。
	OpenAIBPSErrorInvalidToolCall = "bps_invalid_tool_call"
	// OpenAIBPSErrorUnsupportedTool 表示上游返回了本次未声明的工具/条目。
	OpenAIBPSErrorUnsupportedTool = "bps_unsupported_tool"
	// OpenAIBPSErrorParallelTools 表示上游一次返回了多个客户端工具调用。
	OpenAIBPSErrorParallelTools = "bps_parallel_tools_unsupported"
	// OpenAIBPSErrorToolRequired 表示 tool_choice=required 但上游没给工具调用。
	OpenAIBPSErrorToolRequired = "bps_tool_required"
	// OpenAIBPSErrorInvalidResponse 表示上游响应结构非法。
	OpenAIBPSErrorInvalidResponse = "bps_invalid_response"
	// OpenAIBPSErrorUnsupportedEvent 表示上游流里出现未知事件类型。
	OpenAIBPSErrorUnsupportedEvent = "bps_unsupported_event"
	// OpenAIBPSErrorResponseFailed 表示上游明确返回失败/未完成终态。
	OpenAIBPSErrorResponseFailed = "bps_response_failed"
)

// openAIBPSToolProtocol 是写给上游的协议说明（与冻结参考实现逐字对齐）。
// 它要求上游只用 run_officejs 载体请求一个客户端工具，并说明 function 与
// custom 两种信封形状；目录 JSON 会追加在末尾。
const openAIBPSToolProtocol = `This request comes from an external Responses client. Its tools run in the client, not in Excel. Use only the client tools listed below. To request one, call the native run_officejs tool with its code field containing a JSON-encoded object: {"name":"catalog tool name","arguments":{...}} for a function. For a custom tool set summary to exactly codex2api.custom/CATALOG_NAME and put its exact raw input in code, without JSON wrapping or Markdown fences. The marker must include the exact namespace-qualified catalog name. Include the namespace in the catalog tool name. Populate the native wrapper fields summary, extended_summary, destructive=false and references=[]. For functions the code field is JSON text. For custom tools it is opaque client input; the proxy never evaluates it as JavaScript or OfficeJS. Request exactly one client tool at a time. Do not call workbook or Office tools. The proxy converts the request and the client executes it. After receiving its real result, continue the task. Do not repeat a successful tool call. Tool catalog: `

// openAIBPSNoToolProtocol 是没有声明客户端工具时的上游协议说明：禁止上游调用
// 服务端注入的 Excel/Office 工具（否则客户端根本执行不了）。
const openAIBPSNoToolProtocol = `This is an external Responses client without an Excel workbook. Return assistant text and do not call server-injected Excel or Office tools.`

// openAIBPSTool 是一个客户端声明的工具。
type openAIBPSTool struct {
	Name      string
	Namespace string
	Kind      string
	Spec      map[string]any
	Schema    *openAIBPSJSONSchema
}

// openAIBPSPreparedRequest 是请求准备的结果：发往上游的 body + 转换响应所需
// 的客户端工具目录/会话身份/轮次身份。
type openAIBPSPreparedRequest struct {
	APIKeyID      int64
	SessionKey    string
	Source        map[string]any
	Body          []byte
	Tools         map[string]openAIBPSTool
	Catalog       []any
	ClientModel   string
	UpstreamModel string
	Stream        bool
	RequireTool   bool
	// Compact 表示本次请求是压缩（/responses/compact 或 compaction_trigger）。
	// CompactEndpoint 额外记录入口路径，用于把响应 object 标成 response.compaction。
	Compact         bool
	CompactEndpoint bool
	// TurnID/AgentIteration 是本轮身份（由不可变历史与压缩记录派生）。
	TurnID         string
	AgentIteration int
	// Structured 是结构化输出声明（json_object/json_schema）；压缩请求为 nil。
	Structured *openAIBPSStructuredOutput
}

// openAIBPSPrepareOptions 是请求准备的可选入口语义。
type openAIBPSPrepareOptions struct {
	// CompactEndpoint 表示客户端调用的是 /responses/compact。
	CompactEndpoint bool
}

// openAIBPSJSON 是紧凑 JSON 编码（忽略错误，调用点只用于已知可编码的值）。
func openAIBPSJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// openAIBPSDecode 严格解码恰好一个 JSON 值，数字保留为 json.Number（避免大
// 整数精度丢失）。
func openAIBPSDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

// openAIBPSMessage 构造一条 Responses input message。
func openAIBPSMessage(role, text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    role,
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
}

// openAIBPSInvalidRequest 构造 400 请求错误（工具目录/请求结构问题）。
func openAIBPSInvalidRequest(message string) error {
	return infraerrors.Newf(http.StatusBadRequest, OpenAIBPSErrorInvalidRequest, "%s", message)
}

// openAIBPSUpstreamProtocolError 构造 502 上游协议错误。
func openAIBPSUpstreamProtocolError(code, message string) *infraerrors.ApplicationError {
	return infraerrors.Newf(http.StatusBadGateway, code, "%s", message)
}

// openAIBPSStateUnavailable 构造 503 状态存储不可用错误（显式失败，不降级）。
func openAIBPSStateUnavailable() error {
	return infraerrors.Newf(http.StatusServiceUnavailable, OpenAIBPSErrorStateUnavailable,
		"OpenAI BPS conversation state is unavailable")
}

// openAIBPSContextExpired 构造 409 上下文过期错误。
func openAIBPSContextExpired(message string) error {
	return infraerrors.Newf(http.StatusConflict, OpenAIBPSErrorContextExpired, "%s", message)
}

// openAIBPSCollectTools 收集客户端工具声明。
//
// 支持 namespace 嵌套（目录名用 "ns.tool" 全名）、function 与 custom 两种
// 类型；重复声明只有完全一致才合并，不一致按 400 冲突报错；单个请求最多
// 256 个工具，嵌套深度最多 8 层。function 参数 schema 在这里编译（外部
// $ref 直接拒绝）。
func openAIBPSCollectTools(raw any, namespace string, result map[string]openAIBPSTool, catalog *[]any, depth int) error {
	if depth > 8 {
		return openAIBPSInvalidRequest("Tool namespaces are nested too deeply")
	}
	if raw == nil {
		return nil
	}
	tools, ok := raw.([]any)
	if !ok {
		return openAIBPSInvalidRequest("tools must be an array")
	}
	for _, entry := range tools {
		spec, ok := entry.(map[string]any)
		if !ok {
			return openAIBPSInvalidRequest("Invalid tool declaration")
		}
		name := strings.TrimSpace(stringValue(spec["name"]))
		kind := stringValue(spec["type"])
		if name == "" {
			return openAIBPSInvalidRequest("BPS only supports named function and custom client tools")
		}
		full := name
		if namespace != "" {
			full = namespace + "." + name
		}
		if kind == "namespace" {
			if err := openAIBPSCollectTools(spec["tools"], full, result, catalog, depth+1); err != nil {
				return err
			}
			continue
		}
		if kind != "function" && kind != "custom" {
			return openAIBPSInvalidRequest("BPS only supports function, custom and namespace client tools")
		}
		if previous, exists := result[full]; exists {
			if openAIBPSJSON(previous.Spec) != openAIBPSJSON(spec) {
				return openAIBPSInvalidRequest("Conflicting client tool: " + full)
			}
			continue
		}
		if len(result) >= 256 {
			return openAIBPSInvalidRequest("Too many client tools")
		}
		tool := openAIBPSTool{Name: name, Namespace: namespace, Kind: kind, Spec: spec}
		declaration := map[string]any{"name": full, "type": kind, "description": spec["description"]}
		if kind == "function" {
			schema := spec["parameters"]
			if schema == nil {
				schema = spec["inputSchema"]
			}
			if schema == nil {
				schema = spec["input_schema"]
			}
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			compiled, err := openAIBPSCompileJSONSchema(schema)
			if err != nil {
				return openAIBPSInvalidRequest("Invalid schema for " + full + ": " + err.Error())
			}
			tool.Schema = compiled
			declaration["parameters"] = schema
		} else {
			declaration["format"] = spec["format"]
		}
		result[full] = tool
		*catalog = append(*catalog, declaration)
	}
	return nil
}

// prepareOpenAIBPSRequest 把客户端 Responses 请求翻译成 BPS 上游请求。
//
// 与阶段 3 的"原样透传"相比，这里做四件事：
//  1. 收集客户端工具目录（tools + input 里的 additional_tools），并把目录写进
//     developer 协议消息（上游只看到 run_officejs 载体）；
//  2. 回放历史：function_call/custom_tool_call 从 Redis 恢复上游原始 item；
//     客户端结果转换成稳定 fc_ ID 的 function_call_output；
//  3. 加密 reasoning 重建为「类型 + 空 summary + 原密文」；
//  4. 构造白名单化的上游 body（stream/store/reasoning_effort 等）。
//
// account 用于账号级模型映射；store 为 nil 时只有需要回放历史的请求会显式
// 返回 bps_state_unavailable（不降级）。
func prepareOpenAIBPSRequest(ctx context.Context, store *OpenAIBPSSessionStore, account *Account, apiKeyID int64, sessionKey string, body []byte) (*openAIBPSPreparedRequest, error) {
	return prepareOpenAIBPSRequestWithOptions(ctx, store, account, apiKeyID, sessionKey, body, openAIBPSPrepareOptions{})
}

// prepareOpenAIBPSRequestWithOptions 是请求准备的唯一实现：options 只影响
// 入口语义（/responses/compact 强制压缩），其余校验与普通 Responses 一致。
func prepareOpenAIBPSRequestWithOptions(ctx context.Context, store *OpenAIBPSSessionStore, account *Account, apiKeyID int64, sessionKey string, body []byte, options openAIBPSPrepareOptions) (*openAIBPSPreparedRequest, error) {
	var source map[string]any
	if openAIBPSDecode(body, &source) != nil || source == nil {
		return nil, openAIBPSInvalidRequest("Invalid Responses request")
	}
	if strings.TrimSpace(stringValue(source["previous_response_id"])) != "" {
		return nil, openAIBPSInvalidRequest("BPS requires complete input history; previous_response_id is not supported")
	}
	if background, _ := source["background"].(bool); background {
		return nil, openAIBPSInvalidRequest("BPS does not support background responses")
	}
	model := strings.TrimSpace(stringValue(source["model"]))
	if model == "" {
		return nil, openAIBPSInvalidRequest("model is required")
	}
	var input []any
	switch value := source["input"].(type) {
	case string:
		input = []any{openAIBPSMessage("user", value)}
	case []any:
		input = value
	default:
		return nil, openAIBPSInvalidRequest("input must be a string or an array")
	}
	stream, _ := source["stream"].(bool)
	// 压缩入口或 input 里的 compaction_trigger 都触发真实摘要请求。
	compact := options.CompactEndpoint || openAIBPSInputHasCompactionTrigger(input)
	prepared := &openAIBPSPreparedRequest{
		APIKeyID:        apiKeyID,
		SessionKey:      sessionKey,
		Source:          source,
		ClientModel:     model,
		Stream:          stream,
		Tools:           map[string]openAIBPSTool{},
		Compact:         compact,
		CompactEndpoint: options.CompactEndpoint,
	}
	if account != nil {
		prepared.UpstreamModel = account.GetMappedModel(model)
	}
	if prepared.UpstreamModel == "" {
		prepared.UpstreamModel = model
	}

	// 压缩请求关闭客户端工具：不收集 tools、不注入工具目录、忽略 tool_choice。
	// 上游只看到「摘要指令 + 历史」，绝不能被要求调用客户端工具。
	if !compact {
		toolChoice := source["tool_choice"]
		collectTools := true
		if choice, ok := toolChoice.(string); ok && strings.TrimSpace(choice) == "none" {
			collectTools = false
		}
		if collectTools {
			var catalog []any
			if err := openAIBPSCollectTools(source["tools"], "", prepared.Tools, &catalog, 0); err != nil {
				return nil, err
			}
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok || stringValue(item["type"]) != "additional_tools" {
					continue
				}
				if err := openAIBPSCollectTools(item["tools"], "", prepared.Tools, &catalog, 0); err != nil {
					return nil, err
				}
			}
			prepared.Catalog = catalog
		}
		switch choice := toolChoice.(type) {
		case nil:
		case string:
			switch strings.TrimSpace(choice) {
			case "", "auto":
			case "required":
				prepared.RequireTool = true
			case "none":
				prepared.RequireTool = false
				prepared.Tools = map[string]openAIBPSTool{}
				prepared.Catalog = nil
			default:
				return nil, openAIBPSInvalidRequest("Unsupported tool_choice")
			}
		case map[string]any:
			name := stringValue(choice["name"])
			if namespace := stringValue(choice["namespace"]); namespace != "" {
				name = namespace + "." + name
			}
			tool, found := prepared.Tools[name]
			if !found {
				return nil, openAIBPSInvalidRequest("tool_choice must name a declared client tool")
			}
			prepared.Tools = map[string]openAIBPSTool{name: tool}
			var catalog []any
			if err := openAIBPSCollectTools([]any{tool.Spec}, tool.Namespace, map[string]openAIBPSTool{}, &catalog, 0); err != nil {
				return nil, err
			}
			prepared.Catalog = catalog
			prepared.RequireTool = true
		default:
			return nil, openAIBPSInvalidRequest("Unsupported tool_choice")
		}
		if prepared.RequireTool && len(prepared.Tools) == 0 {
			return nil, openAIBPSInvalidRequest("tool_choice requires at least one client tool")
		}
		// 结构化输出只作用于普通 Responses；压缩必须返回真实文本摘要。
		structured, err := openAIBPSPrepareStructuredOutput(source["text"])
		if err != nil {
			return nil, err
		}
		prepared.Structured = structured
	}

	translated, cursor, err := openAIBPSTranslateInput(ctx, store, prepared, input)
	if err != nil {
		return nil, err
	}
	prologue := make([]any, 0, 2)
	if instructions := stringValue(source["instructions"]); instructions != "" {
		prologue = append(prologue, openAIBPSMessage("developer", instructions))
	}
	protocol := openAIBPSNoToolProtocol
	if len(prepared.Tools) > 0 {
		protocol = openAIBPSToolProtocol + openAIBPSJSON(prepared.Catalog)
		if prepared.RequireTool {
			protocol += " You must call one of the listed tools in this response."
		}
	}
	if prepared.Structured != nil {
		protocol += "\n" + prepared.Structured.instructions()
	}
	prologue = append(prologue, openAIBPSMessage("developer", protocol))
	translated = append(prologue, translated...)
	if compact {
		// 真实摘要请求：最后一条用户消息是摘要指令，不是客户端历史的一部分。
		translated = append(translated, openAIBPSMessage("user", openAIBPSSummaryPrompt))
	}

	effort, err := openAIBPSReasoningEffort(source)
	if err != nil {
		return nil, err
	}
	// 轮次身份只由原始历史与压缩记录派生：重试同一请求不会改变上游 body。
	prepared.TurnID, prepared.AgentIteration = openAIBPSTurnState(sessionKey, input, cursor.Index, cursor.Ref, cursor.State)
	taskID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("openai_bps:"+sessionKey)).String()
	upstream := map[string]any{
		"model":            prepared.UpstreamModel,
		"model_selection":  "explicit",
		"stream":           stream,
		"store":            false,
		"input":            translated,
		"reasoning_effort": effort,
		"metadata": map[string]any{
			"task_id":         taskID,
			"turn_id":         prepared.TurnID,
			"agent_iteration": fmt.Sprint(prepared.AgentIteration),
		},
		"prompt_cache_key": sessionKey,
	}
	// 结构化输出用 developer 提示词约束 + 本地终态校验实现；原生 text.format
	// 绝不发给上游（BPS 不支持约束解码）。
	encoded, err := json.Marshal(upstream)
	if err != nil {
		return nil, fmt.Errorf("openai bps: encode upstream request: %w", err)
	}
	prepared.Body = encoded
	return prepared, nil
}

// openAIBPSTranslateInput 把客户端历史翻译成上游 input 数组。
//
// 返回的游标记录历史里最近一条压缩引用（供轮次锚点使用）。压缩引用只在
// 服务端解析：上游看到的是 <conversation_summary> 包裹的摘要文本，引用
// 字符串本身绝不进入上游 body。
func openAIBPSTranslateInput(ctx context.Context, store *OpenAIBPSSessionStore, prepared *openAIBPSPreparedRequest, input []any) ([]any, openAIBPSCompactionCursor, error) {
	cursor := openAIBPSCompactionCursor{Index: -1}
	translated := make([]any, 0, len(input)+2)
	restoredCalls := map[string]bool{}
	for index, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, cursor, openAIBPSInvalidRequest("input items must be objects")
		}
		delete(item, "internal_chat_message_metadata_passthrough")
		kind := stringValue(item["type"])
		switch kind {
		case "function_call", "custom_tool_call":
			callID := strings.TrimSpace(stringValue(item["call_id"]))
			if callID == "" {
				return nil, cursor, openAIBPSInvalidRequest("BPS tool history requires call_id")
			}
			saved, err := openAIBPSLoadToolItemForReplay(ctx, store, prepared, callID)
			if err != nil {
				return nil, cursor, err
			}
			translated = append(translated, saved.Native)
			restoredCalls[callID] = true
		case "function_call_output", "custom_tool_call_output":
			callID := strings.TrimSpace(stringValue(item["call_id"]))
			if callID == "" {
				return nil, cursor, openAIBPSInvalidRequest("BPS tool history requires call_id")
			}
			saved, err := openAIBPSLoadToolItemForReplay(ctx, store, prepared, callID)
			if err != nil {
				return nil, cursor, err
			}
			if !restoredCalls[callID] {
				translated = append(translated, saved.Native)
				restoredCalls[callID] = true
			}
			output := item["output"]
			if openAIBPSIsNativePlan(stringValue(saved.Native["name"])) && openAIBPSIsPlanUpdatedOutput(output) {
				// update_plan 的成功回执：上游只需要一个稳定成功信号。
				output = map[string]any{"status": "ok"}
			}
			translated = append(translated, map[string]any{
				"type":    "function_call_output",
				"id":      openAIBPSFunctionOutputID(callID),
				"call_id": callID,
				"output":  output,
			})
		case "reasoning":
			if encrypted := strings.TrimSpace(stringValue(item["encrypted_content"])); encrypted != "" {
				translated = append(translated, map[string]any{
					"type":              "reasoning",
					"summary":           []any{},
					"encrypted_content": encrypted,
				})
			}
		case "compaction", "compaction_summary":
			ref := strings.TrimSpace(stringValue(item["encrypted_content"]))
			if ref == "" {
				if prepared.Compact {
					// 压缩请求里的空引用占位：没有可恢复的上下文，忽略。
					continue
				}
				return nil, cursor, openAIBPSInvalidRequest("BPS compaction item is missing its reference")
			}
			if !openAIBPSCompactPrefixMatches(ref) {
				return nil, cursor, openAIBPSInvalidRequest("BPS cannot replay compaction created by another provider")
			}
			state, err := openAIBPSLoadCompactState(ctx, store, prepared, ref)
			if err != nil {
				return nil, cursor, err
			}
			// 引用只在这里解析：上游只看到摘要文本，永远看不到引用字符串。
			translated = append(translated, openAIBPSMessage("user", "<conversation_summary>\n"+state.Summary+"\n</conversation_summary>"))
			cursor = openAIBPSCompactionCursor{Index: index, Ref: ref, State: state}
		case "compaction_trigger", "additional_tools":
			// 服务端信号/工具目录载体，不透传给上游。
			continue
		case "item_reference":
			return nil, cursor, openAIBPSInvalidRequest("BPS requires full items instead of item_reference")
		default:
			if parts, ok := item["content"].([]any); ok {
				for _, rawPart := range parts {
					part, ok := rawPart.(map[string]any)
					if !ok {
						continue
					}
					switch stringValue(part["type"]) {
					case "input_image", "input_audio", "input_file":
						return nil, cursor, openAIBPSInvalidRequest("BPS currently supports text input only")
					}
				}
			}
			translated = append(translated, item)
		}
	}
	return translated, cursor, nil
}

// openAIBPSReasoningEffort 解析客户端的 reasoning effort（顶层或 reasoning 对象）。
func openAIBPSReasoningEffort(source map[string]any) (string, error) {
	effort := "medium"
	raw := source["reasoning_effort"]
	if reasoning, exists := source["reasoning"]; exists && reasoning != nil {
		object, ok := reasoning.(map[string]any)
		if !ok {
			return "", openAIBPSInvalidRequest("reasoning must be an object")
		}
		if value, exists := object["effort"]; exists {
			raw = value
		}
	}
	if raw != nil {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return "", openAIBPSInvalidRequest("BPS reasoning effort must be low, medium, high or xhigh")
		}
		effort = strings.TrimSpace(value)
	}
	if effort == "x-high" {
		effort = "xhigh"
	}
	switch effort {
	case "low", "medium", "high", "xhigh":
		return effort, nil
	default:
		return "", openAIBPSInvalidRequest("BPS reasoning effort must be low, medium, high or xhigh")
	}
}

// openAIBPSLoadToolItemForReplay 读取 Redis 里的工具 item；缺失即上下文过期，
// 绝不静默重建（重建会在会话中途改变上游看到的历史）。
func openAIBPSLoadToolItemForReplay(ctx context.Context, store *OpenAIBPSSessionStore, prepared *openAIBPSPreparedRequest, callID string) (*openAIBPSToolItem, error) {
	if store == nil {
		return nil, openAIBPSStateUnavailable()
	}
	item, err := store.LoadToolItem(ctx, prepared.APIKeyID, prepared.SessionKey, callID)
	if err == nil {
		return item, nil
	}
	switch {
	case errors.Is(err, ErrOpenAIBPSSessionStoreUnavailable):
		return nil, openAIBPSStateUnavailable()
	case errors.Is(err, ErrOpenAIBPSToolItemExpired):
		return nil, openAIBPSContextExpired("BPS tool call state has expired; start a new conversation")
	default:
		return nil, openAIBPSStateUnavailable()
	}
}

// openAIBPSIsNativePlan 报告上游工具名是否是原生 update_plan。
func openAIBPSIsNativePlan(name string) bool {
	return name == "update_plan" || name == "functions.update_plan"
}

// openAIBPSIsPlanUpdatedOutput 识别客户端 update_plan 的成功回执。
func openAIBPSIsPlanUpdatedOutput(output any) bool {
	return strings.EqualFold(strings.TrimSpace(stringValue(output)), "Plan updated")
}

// openAIBPSPlanArguments 归一化原生 update_plan 参数：步骤别名
// （step/description/title）、状态别名（done→completed 等）、explanation/summary。
// 无法归一化的输入按工具调用非法处理（绝不把不一致的 plan 交给客户端）。
func openAIBPSPlanArguments(raw map[string]any) (map[string]any, error) {
	textAlias := func(item map[string]any, keys ...string) (string, error) {
		value := ""
		present := false
		for _, key := range keys {
			if candidate, exists := item[key]; exists {
				text, ok := candidate.(string)
				if !ok || (present && value != text) {
					return "", errors.New("ambiguous plan text fields")
				}
				value, present = text, true
			}
		}
		return value, nil
	}
	plan, ok := raw["plan"].([]any)
	if !ok {
		return nil, errors.New("native update_plan requires a plan array")
	}
	normalized := make([]any, 0, len(plan))
	for _, entry := range plan {
		step, ok := entry.(map[string]any)
		if !ok {
			return nil, errors.New("invalid native plan entry")
		}
		text, err := textAlias(step, "step", "description", "title")
		if err != nil || strings.TrimSpace(text) == "" {
			return nil, errors.New("missing or conflicting native plan step")
		}
		status := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(stringValue(step["status"]))))
		switch status {
		case "pending", "not_started", "todo", "planned", "queued", "blocked":
			status = "pending"
		case "in_progress", "inprogress", "running", "active", "started", "doing", "current":
			status = "in_progress"
		case "completed", "done", "complete", "finished":
			status = "completed"
		default:
			return nil, errors.New("unsupported native plan status")
		}
		normalized = append(normalized, map[string]any{"step": text, "status": status})
	}
	result := map[string]any{"plan": normalized}
	explanation, err := textAlias(raw, "explanation", "summary")
	if err != nil {
		return nil, err
	}
	if explanation != "" {
		result["explanation"] = explanation
	}
	return result, nil
}

// openAIBPSConvertToolCall 把上游 run_officejs 载体（或原生 update_plan）转换成
// 客户端 Responses 工具调用 item。
//
// 返回转换后的客户端 item 与命中的工具声明（调用方据此保存 Redis 原始 item）。
func openAIBPSConvertToolCall(native map[string]any, prepared *openAIBPSPreparedRequest) (map[string]any, *openAIBPSTool, error) {
	invalid := func(message string) (map[string]any, *openAIBPSTool, error) {
		return nil, nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidToolCall, message)
	}
	unsupported := func(message string) (map[string]any, *openAIBPSTool, error) {
		return nil, nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorUnsupportedTool, message)
	}
	nativeName := stringValue(native["name"])
	name := nativeName
	var args map[string]any
	if value, ok := native["arguments"].(map[string]any); ok {
		args = value
	} else if openAIBPSDecode([]byte(stringValue(native["arguments"])), &args) != nil {
		return invalid("BPS returned malformed tool arguments")
	}
	if args == nil {
		return invalid("BPS returned empty tool arguments")
	}
	var envelope map[string]any
	marked := false
	if nativeName == openAIBPSNativeToolName || nativeName == openAIBPSNativeToolNameQualified {
		var err error
		envelope, marked, err = openAIBPSCustomTransportEnvelope(args)
		if err == nil && !marked {
			envelope, err = openAIBPSDecodeTransportEnvelope(args["code"])
		}
		if err != nil {
			return invalid(err.Error())
		}
		name, err = openAIBPSEnvelopeName(envelope)
		if err != nil {
			return invalid(err.Error())
		}
	} else if !openAIBPSIsNativePlan(nativeName) {
		return unsupported("BPS returned an undeclared server tool")
	}
	tool, ok := prepared.Tools[name]
	if !ok && openAIBPSIsNativePlan(nativeName) {
		for _, candidate := range prepared.Tools {
			if candidate.Name != "update_plan" {
				continue
			}
			if ok {
				return invalid("Ambiguous update_plan namespace")
			}
			tool, ok = candidate, true
		}
	}
	if !ok {
		return unsupported("BPS returned a tool not declared by the client")
	}
	callID, itemID := stringValue(native["call_id"]), stringValue(native["id"])
	if strings.TrimSpace(callID) == "" || strings.TrimSpace(callID) != callID || itemID == "" {
		return invalid("BPS tool call is missing its native identity")
	}
	if marked && tool.Kind != "custom" {
		return invalid("Raw tool transport requires a declared custom tool")
	}
	result := map[string]any{
		"type":    "function_call",
		"id":      itemID,
		"call_id": callID,
		"name":    tool.Name,
		"status":  "completed",
	}
	if tool.Namespace != "" {
		result["namespace"] = tool.Namespace
	}
	if tool.Kind == "custom" {
		value, exists := envelope["input"]
		if alias, hasAlias := envelope["args"]; hasAlias {
			if exists {
				return invalid("BPS custom envelope contains conflicting input fields")
			}
			value = alias
		}
		if _, exists := envelope["arguments"]; exists {
			return invalid("BPS custom tools require raw input, not function arguments")
		}
		input, ok := value.(string)
		if !ok || len(input) > openAIBPSMaxEnvelopeBytes {
			return invalid("BPS custom tool input must be text within 1 MiB")
		}
		result["type"] = "custom_tool_call"
		result["id"] = "ctc_" + callID
		result["input"] = input
		return result, &tool, nil
	}
	if envelope != nil {
		value, err := openAIBPSEnvelopeArguments(envelope)
		if err != nil {
			return invalid(err.Error())
		}
		if text, ok := value.(string); ok {
			if len(text) > openAIBPSMaxEnvelopeBytes || openAIBPSDecode([]byte(text), &value) != nil {
				return invalid("BPS function arguments must be one JSON object")
			}
		}
		args, _ = value.(map[string]any)
	} else {
		var err error
		args, err = openAIBPSPlanArguments(args)
		if err != nil {
			return invalid(err.Error())
		}
	}
	if args == nil || tool.Schema == nil || tool.Schema.Validate(args) != nil {
		return invalid("BPS tool arguments do not match the client schema")
	}
	result["arguments"] = openAIBPSJSON(args)
	return result, &tool, nil
}

// openAIBPSTransformResponse 把上游完成的响应转换成客户端 Responses 响应：
// 压缩响应保存摘要并只回写不透明引用；结构化响应先做终态校验再回写声明；
// 普通响应转换工具 item、保存 Redis 原始 item、做单工具/必选工具终态校验，
// 并把 model 还原成客户端请求的模型名。
func openAIBPSTransformResponse(ctx context.Context, store *OpenAIBPSSessionStore, prepared *openAIBPSPreparedRequest, response map[string]any) (map[string]any, error) {
	output, ok := response["output"].([]any)
	if !ok {
		return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS response has no output array")
	}
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidResponse, "BPS returned a malformed output item")
		}
		switch stringValue(item["type"]) {
		case "message", "reasoning", "function_call", "custom_tool_call":
		default:
			return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorUnsupportedTool, "BPS returned an unsupported output item")
		}
	}
	if prepared.Compact {
		return openAIBPSCompactResponse(ctx, store, prepared, response, output)
	}
	if prepared.Structured != nil {
		// 终态本地校验：只有校验通过才把文本交给客户端（JSON 与 SSE 同一规则）。
		if err := prepared.Structured.validate(response); err != nil {
			return nil, err
		}
		config, _ := response["text"].(map[string]any)
		if config == nil {
			config = map[string]any{}
		}
		config["format"] = prepared.Structured.Format
		response["text"] = config
		delete(response, "output_text")
	}
	toolCount := 0
	for _, raw := range output {
		if item, ok := raw.(map[string]any); ok {
			switch stringValue(item["type"]) {
			case "function_call", "custom_tool_call":
				toolCount++
			}
		}
	}
	if toolCount > 1 {
		return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorParallelTools, "BPS must return one client tool at a time")
	}
	if prepared.RequireTool && toolCount == 0 {
		return nil, openAIBPSUpstreamProtocolError(OpenAIBPSErrorToolRequired, "BPS did not return the required client tool")
	}
	for index, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if kind := stringValue(item["type"]); kind != "function_call" && kind != "custom_tool_call" {
			continue
		}
		client, tool, err := openAIBPSConvertToolCall(item, prepared)
		if err != nil {
			return nil, err
		}
		callID := stringValue(item["call_id"])
		if store == nil {
			return nil, openAIBPSStateUnavailable()
		}
		saved := &openAIBPSToolItem{
			CallID:    callID,
			ItemID:    stringValue(item["id"]),
			Name:      tool.Name,
			Namespace: tool.Namespace,
			Kind:      tool.Kind,
			Native:    item,
			Arguments: openAIBPSClientArgumentsRaw(client),
			Catalog:   prepared.Catalog,
			SavedAt:   time.Now().Unix(),
		}
		if err := store.SaveToolItem(ctx, prepared.APIKeyID, prepared.SessionKey, saved); err != nil {
			if errors.Is(err, ErrOpenAIBPSSessionStoreUnavailable) {
				return nil, openAIBPSStateUnavailable()
			}
			return nil, openAIBPSStateUnavailable()
		}
		output[index] = client
	}
	response["output"] = output
	response["model"] = prepared.ClientModel
	return response, nil
}

// openAIBPSClientArgumentsRaw 提取客户端可见的 arguments（function 为校验后的 JSON
// 文本；custom 为原文输入的 JSON 字符串编码），便于审计与回放排障。
func openAIBPSClientArgumentsRaw(client map[string]any) json.RawMessage {
	if text, ok := client["arguments"].(string); ok && json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	if input, ok := client["input"].(string); ok {
		if encoded, err := json.Marshal(input); err == nil {
			return encoded
		}
	}
	return nil
}

// openAIBPSFunctionOutputID 生成稳定的 function_call_output item ID。
// 与参考实现一致：fc_<callID>（≤64 字符），过长时用摘要截断，保证回放幂等。
func openAIBPSFunctionOutputID(callID string) string {
	candidate := "fc_" + callID
	if len(candidate) <= 64 {
		return candidate
	}
	sum := sha256.Sum256([]byte(callID))
	return "fc_" + hex.EncodeToString(sum[:])[:61]
}
