package service

// BPS 结构化输出（阶段 6）。
//
// BPS 上游不支持原生约束解码，因此 text.format 的 json_object/json_schema 由
// 两层保证：
//   - 请求侧：把「必须返回一个 JSON 值」的要求写进 developer 协议消息，原生
//     text.format 绝不发给上游；
//   - 响应侧：终态文本在本地按 schema 校验（即使 strict=false 也校验），
//     通过后才交付客户端；失败返回 bps_invalid_structured_output。
//
// 校验复用本仓库自研的 JSON Schema 引擎（openai_bps_schema.go）：外部 $ref
// 一律拒绝、深度有上限、数字按 json.Number 保留精度。
import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// OpenAIBPSErrorInvalidStructuredOutput 表示最终文本不满足声明的输出格式。
	OpenAIBPSErrorInvalidStructuredOutput = "bps_invalid_structured_output"

	// openAIBPSMaxStructuredSchemaBytes 是结构化输出 schema 的大小上限。
	openAIBPSMaxStructuredSchemaBytes = 1 << 20
	// openAIBPSMaxStructuredTextBytes 是结构化最终文本的大小上限。
	openAIBPSMaxStructuredTextBytes = 16 << 20
)

// openAIBPSStructuredNamePattern 是 json_schema.name 的合法字符集。
var openAIBPSStructuredNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// openAIBPSStructuredTypes 是 JSON Schema 允许的 type 名称。
var openAIBPSStructuredTypes = map[string]bool{
	"object": true, "array": true, "string": true, "number": true,
	"integer": true, "boolean": true, "null": true,
}

// openAIBPSStructuredOutput 是解析后的 text.format 声明。
type openAIBPSStructuredOutput struct {
	Format map[string]any
	Schema *openAIBPSJSONSchema
}

// openAIBPSPrepareStructuredOutput 解析并校验客户端声明的 text.format。
//
// 返回 nil 表示没有结构化要求（未声明/text 类型）。所有声明错误都是 400：
// 在触碰上游之前暴露，避免把不可校验的请求发出去。
func openAIBPSPrepareStructuredOutput(raw any) (*openAIBPSStructuredOutput, error) {
	if raw == nil {
		return nil, nil
	}
	config, ok := raw.(map[string]any)
	if !ok {
		return nil, openAIBPSInvalidRequest("text must be an object")
	}
	if config["format"] == nil {
		return nil, nil
	}
	format, ok := config["format"].(map[string]any)
	if !ok {
		return nil, openAIBPSInvalidRequest("text.format must be an object")
	}
	kind := stringValue(format["type"])
	if kind == "text" {
		return nil, nil
	}
	if kind != "json_object" && kind != "json_schema" {
		return nil, openAIBPSInvalidRequest("text.format.type must be text, json_object or json_schema")
	}
	for key := range format {
		if key == "type" {
			continue
		}
		if kind == "json_schema" && (key == "name" || key == "schema" || key == "strict" || key == "description") {
			continue
		}
		return nil, openAIBPSInvalidRequest("Unsupported text.format field: " + key)
	}
	result := &openAIBPSStructuredOutput{Format: format}
	if kind == "json_object" {
		return result, nil
	}
	if !openAIBPSStructuredNamePattern.MatchString(stringValue(format["name"])) {
		return nil, openAIBPSInvalidRequest("json_schema requires a name of 1-64 letters, digits, underscores or hyphens")
	}
	if value := format["strict"]; value != nil {
		if _, ok := value.(bool); !ok {
			return nil, openAIBPSInvalidRequest("json_schema strict must be a boolean")
		}
	}
	if value := format["description"]; value != nil {
		if _, ok := value.(string); !ok {
			return nil, openAIBPSInvalidRequest("json_schema description must be text")
		}
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok || schema == nil {
		return nil, openAIBPSInvalidRequest("json_schema requires a schema object")
	}
	encoded := openAIBPSJSON(schema)
	if len(encoded) > openAIBPSMaxStructuredSchemaBytes {
		return nil, openAIBPSInvalidRequest("BPS structured output schema exceeds 1 MiB")
	}
	if err := openAIBPSValidateStructuredSchemaShape(schema, 0); err != nil {
		return nil, openAIBPSInvalidRequest("Invalid structured output schema: " + err.Error())
	}
	compiled, err := openAIBPSCompileJSONSchema(schema)
	if err != nil {
		return nil, openAIBPSInvalidRequest("Invalid structured output schema or external reference")
	}
	result.Schema = compiled
	return result, nil
}

// openAIBPSValidateStructuredSchemaShape 在编译前做结构校验：type 必须是
// 已知 JSON Schema 类型，schema 容器关键字必须是正确形状。工具参数 schema
// 允许更宽松的注解，这里对结构化输出更严格（客户端错误应在请求阶段暴露）。
func openAIBPSValidateStructuredSchemaShape(node any, depth int) error {
	if depth > openAIBPSMaxSchemaDepth {
		return errors.New("schema is nested too deeply")
	}
	switch value := node.(type) {
	case bool:
		return nil
	case map[string]any:
		if rawType, exists := value["type"]; exists {
			switch typ := rawType.(type) {
			case string:
				if !openAIBPSStructuredTypes[typ] {
					return fmt.Errorf("unsupported schema type %q", typ)
				}
			case []any:
				if len(typ) == 0 {
					return errors.New("schema type array must not be empty")
				}
				for _, entry := range typ {
					name, ok := entry.(string)
					if !ok || !openAIBPSStructuredTypes[name] {
						return errors.New("schema type array must contain JSON Schema type names")
					}
				}
			default:
				return errors.New("schema type must be a string or an array of strings")
			}
		}
		if rawRequired, exists := value["required"]; exists {
			required, ok := rawRequired.([]any)
			if !ok {
				return errors.New("schema required must be an array")
			}
			for _, entry := range required {
				if _, ok := entry.(string); !ok {
					return errors.New("schema required entries must be strings")
				}
			}
		}
		if rawEnum, exists := value["enum"]; exists {
			if _, ok := rawEnum.([]any); !ok {
				return errors.New("schema enum must be an array")
			}
		}
		// 只有这些关键字的值是 schema（或 schema 集合），其余是取值/注解。
		for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
			raw, exists := value[key]
			if !exists {
				continue
			}
			object, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("schema %s must be an object", key)
			}
			for name, child := range object {
				if err := openAIBPSValidateStructuredSchemaShape(child, depth+1); err != nil {
					return fmt.Errorf("%s.%s: %w", key, name, err)
				}
			}
		}
		for _, key := range []string{"additionalProperties", "items", "not", "if", "then", "else", "contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema"} {
			raw, exists := value[key]
			if !exists {
				continue
			}
			if children, ok := raw.([]any); ok {
				if key != "items" {
					return fmt.Errorf("schema %s must be an object or boolean", key)
				}
				// 旧版 tuple 写法。
				for _, child := range children {
					if err := openAIBPSValidateStructuredSchemaShape(child, depth+1); err != nil {
						return fmt.Errorf("%s: %w", key, err)
					}
				}
				continue
			}
			if err := openAIBPSValidateStructuredSchemaShape(raw, depth+1); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
			raw, exists := value[key]
			if !exists {
				continue
			}
			children, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("schema %s must be an array", key)
			}
			for _, child := range children {
				if err := openAIBPSValidateStructuredSchemaShape(child, depth+1); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		}
		return nil
	case []any:
		for _, child := range value {
			if err := openAIBPSValidateStructuredSchemaShape(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("schema nodes must be objects or booleans")
	}
}

// instructions 是写进 developer 协议消息的结构化输出要求。
func (s *openAIBPSStructuredOutput) instructions() string {
	instruction := "The client requires a structured final answer. Return exactly one JSON object without Markdown fences or surrounding prose. Tool calls and explicit refusals remain separate protocol items; use declared client tools as needed before the final answer. The proxy validates your final answer before returning it."
	if s.Schema != nil {
		instruction = "The client requires a structured final answer. Return exactly one JSON value without Markdown fences or surrounding prose, satisfying this output format: " + openAIBPSJSON(s.Format) + ". Tool calls and explicit refusals remain separate protocol items; use declared client tools as needed before the final answer. The proxy validates your final answer before returning it."
	}
	return instruction
}

// openAIBPSInvalidStructured 构造 502 结构化输出校验失败错误。
func openAIBPSInvalidStructured(message string) error {
	return openAIBPSUpstreamProtocolError(OpenAIBPSErrorInvalidStructuredOutput, message)
}

// validate 校验终态响应里的最终文本。
//
// 工具调用与明确拒答是独立协议项：出现工具调用时不校验文本（下一轮还会
// 继续）；纯拒答（没有文本）也不要求 JSON。其余情况必须恰好一个 JSON 值，
// json_object 必须是对象，json_schema 必须满足 schema（strict 只影响提示词
// 语义，本地始终校验）。
func (s *openAIBPSStructuredOutput) validate(response map[string]any) error {
	output, _ := response["output"].([]any)
	var answer strings.Builder
	hasTool, hasRefusal := false, false
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		kind := stringValue(item["type"])
		hasTool = hasTool || kind == "function_call" || kind == "custom_tool_call"
		if kind != "message" {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			return openAIBPSInvalidStructured("BPS structured message has invalid content")
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return openAIBPSInvalidStructured("BPS structured message has invalid content")
			}
			switch stringValue(part["type"]) {
			case "output_text":
				value, ok := part["text"].(string)
				if !ok || len(value) > openAIBPSMaxStructuredTextBytes-answer.Len() {
					return openAIBPSInvalidStructured("BPS structured answer is invalid or exceeds 16 MiB")
				}
				_, _ = answer.WriteString(value)
			case "refusal":
				if _, ok := part["refusal"].(string); !ok {
					return openAIBPSInvalidStructured("BPS refusal must contain text")
				}
				hasRefusal = true
			default:
				return openAIBPSInvalidStructured("BPS structured answer contains unsupported message content")
			}
		}
	}
	if hasTool || (hasRefusal && answer.Len() == 0) {
		return nil
	}
	var value any
	if openAIBPSDecode([]byte(answer.String()), &value) != nil {
		return openAIBPSInvalidStructured("BPS final answer is not one valid JSON value")
	}
	if s.Schema == nil {
		if _, ok := value.(map[string]any); !ok {
			return openAIBPSInvalidStructured("BPS json_object answer must be an object")
		}
		return nil
	}
	if s.Schema.Validate(value) != nil {
		return openAIBPSInvalidStructured("BPS final answer does not satisfy the requested JSON schema")
	}
	return nil
}

// openAIBPSStructuredMessageEvent 报告哪些上游事件属于「待校验的最终文本」：
// 这些事件必须缓冲到终态校验通过后才交付，普通文本仍然实时转发。
func openAIBPSStructuredMessageEvent(typ string, item map[string]any) bool {
	return strings.HasPrefix(typ, "response.output_text.") ||
		strings.HasPrefix(typ, "response.refusal.") ||
		strings.HasPrefix(typ, "response.content_part.") ||
		(strings.HasPrefix(typ, "response.output_item.") && stringValue(item["type"]) == "message")
}
