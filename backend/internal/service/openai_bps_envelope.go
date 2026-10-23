package service

// BPS 工具信封编解码。
//
// 上游 BPS 只认识服务端注入的 run_officejs 协议载体，客户端声明的工具必须
// 编码进这个载体里。本文件负责两个方向：
//
//   - 编码（请求/测试用）：把一次客户端工具调用编码成 run_officejs 的
//     arguments，function 用 JSON 信封（name/arguments），custom 用
//     summary=codex2api.custom/<完整工具名> + code=原文；
//   - 解码（响应侧）：从 run_officejs 的 arguments 中还原出客户端工具调用，
//     兼容 JSON 字符串多层包裹、Markdown 围栏、tool/args 别名，但绝不求值
//     客户端代码（OfficeJS/JavaScript 一律不执行）。
//
// 所有信封都受 1 MiB 上限约束：超过上限直接拒绝，而不是截断。
import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	// openAIBPSMaxEnvelopeBytes 是单个工具信封（function arguments 文本或
	// custom 原文输入）的上限。
	openAIBPSMaxEnvelopeBytes = 1 << 20

	// openAIBPSNativeToolName / openAIBPSNativeToolNameQualified 是上游唯一
	// 可用的协议载体工具名（不同客户端命名空间写法）。
	openAIBPSNativeToolName          = "run_officejs"
	openAIBPSNativeToolNameQualified = "functions.run_officejs"

	// openAIBPSCustomTransportPrefix 是 custom 工具"原文透传"信封的 summary
	// 前缀：summary=codex2api.custom/<完整工具名>，code=客户端原文。
	openAIBPSCustomTransportPrefix = "codex2api.custom/"
)

// openAIBPSEncodeFunctionEnvelope 把一次 function 工具调用编码成默认
// name/arguments JSON 信封。
func openAIBPSEncodeFunctionEnvelope(name string, arguments map[string]any) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("openai bps envelope: function name is empty")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("openai bps envelope: encode arguments: %w", err)
	}
	if len(encoded) > openAIBPSMaxEnvelopeBytes {
		return nil, fmt.Errorf("openai bps envelope: arguments exceed %d bytes", openAIBPSMaxEnvelopeBytes)
	}
	return map[string]any{"name": name, "arguments": arguments}, nil
}

// openAIBPSEncodeCustomEnvelope 把一次 custom 工具调用编码成原文透传信封
// （summary 标记 + code 原文）。
func openAIBPSEncodeCustomEnvelope(name, input string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsSpace) >= 0 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, errors.New("openai bps envelope: custom tool requires an exact nonempty name")
	}
	if len(input) > openAIBPSMaxEnvelopeBytes {
		return nil, fmt.Errorf("openai bps envelope: custom input exceeds %d bytes", openAIBPSMaxEnvelopeBytes)
	}
	return map[string]any{
		"summary": openAIBPSCustomTransportPrefix + name,
		"code":    input,
	}, nil
}

// openAIBPSCustomTransportEnvelope 识别显式标记的 custom 原文透传信封。
//
// 只认 summary 前缀精确匹配：普通 run_officejs 调用继续走 JSON 解码路径。
// 返回 marked=true 表示 summary 带标记（无论内容是否合法），此时调用方必须
// 拒绝 function 工具使用该载体。
func openAIBPSCustomTransportEnvelope(arguments map[string]any) (map[string]any, bool, error) {
	summary, ok := arguments["summary"].(string)
	if !ok || !strings.HasPrefix(summary, openAIBPSCustomTransportPrefix) {
		return nil, false, nil
	}
	name := strings.TrimPrefix(summary, openAIBPSCustomTransportPrefix)
	if name == "" || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsSpace) >= 0 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, true, errors.New("basispoints raw custom transport requires an exact nonempty catalog tool name in summary")
	}
	input, ok := arguments["code"].(string)
	if !ok {
		return nil, true, errors.New("basispoints raw custom transport code must be a string")
	}
	if len(input) > openAIBPSMaxEnvelopeBytes {
		return nil, true, fmt.Errorf("basispoints raw custom transport code exceeds %d bytes", openAIBPSMaxEnvelopeBytes)
	}
	return map[string]any{"name": name, "input": input}, true, nil
}

// openAIBPSDecodeTransportEnvelope 从 run_officejs 的 arguments 中还原客户端
// 工具信封。允许最多两层 run_officejs 嵌套（不同上游版本会多包一层），但
// 每层都必须是一个完整 JSON 值。
func openAIBPSDecodeTransportEnvelope(value any) (map[string]any, error) {
	for depth := 0; depth < 3; depth++ {
		envelope, err := openAIBPSDecodeTransportCode(value)
		if err != nil {
			return nil, err
		}
		name, err := openAIBPSEnvelopeName(envelope)
		if err != nil {
			return nil, err
		}
		if name != openAIBPSNativeToolName && name != openAIBPSNativeToolNameQualified {
			return envelope, nil
		}
		args, err := openAIBPSEnvelopeArguments(envelope)
		if err != nil {
			return nil, err
		}
		if raw, ok := args.(string); ok {
			var parsed map[string]any
			if openAIBPSDecode([]byte(raw), &parsed) != nil {
				return nil, errors.New("basispoints nested transport arguments must be one JSON object")
			}
			args = parsed
		}
		outer, ok := args.(map[string]any)
		if !ok {
			return nil, errors.New("basispoints nested transport arguments must be an object")
		}
		value = outer["code"]
	}
	return nil, errors.New("basispoints tool transport exceeds two nested wrappers")
}

// openAIBPSDecodeTransportCode 剥掉模型常见的格式化包装（JSON 字符串、
// Markdown 围栏、短前缀说明），每一层都必须恰好是一个完整 JSON 值。
func openAIBPSDecodeTransportCode(value any) (map[string]any, error) {
	original := value
	for depth := 0; depth < 4; depth++ {
		if item, ok := value.(map[string]any); ok && item != nil {
			if len(openAIBPSJSON(item)) > openAIBPSMaxEnvelopeBytes {
				break
			}
			return item, nil
		}
		raw, ok := value.(string)
		if !ok || len(raw) > openAIBPSMaxEnvelopeBytes {
			break
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			break
		}
		if decoded, ok := openAIBPSDecodeEnvelopeValue(raw); ok {
			value = decoded
			continue
		}
		// 完整的 Markdown 围栏，前面允许一小段无标点的说明文字。
		if start := strings.Index(raw, "```"); start >= 0 && openAIBPSProsePrefix(raw[:start]) {
			fenced := raw[start:]
			newline := strings.IndexByte(fenced, '\n')
			if newline >= 0 && strings.HasSuffix(fenced, "```") {
				language := strings.TrimSpace(fenced[3:newline])
				if language == "" || strings.EqualFold(language, "json") {
					value = strings.TrimSpace(fenced[newline+1 : len(fenced)-3])
					continue
				}
			}
		}
		// 前缀说明 + JSON 对象。
		if start := strings.IndexByte(raw, '{'); start > 0 && openAIBPSProsePrefix(raw[:start]) {
			if decoded, ok := openAIBPSDecodeEnvelopeValue(raw[start:]); ok {
				value = decoded
				continue
			}
		}
		break
	}
	return nil, fmt.Errorf("basispoints tool transport code must contain one JSON client-tool envelope; OfficeJS and multiple calls are unsupported (%s)", openAIBPSTransportShape(original))
}

// openAIBPSTransportShape 只报告结构事实，绝不回显客户端代码/提示词/参数。
func openAIBPSTransportShape(value any) string {
	raw, ok := value.(string)
	if !ok {
		if value == nil {
			return "format=missing"
		}
		return fmt.Sprintf("format=non_string_%T", value)
	}
	trimmed := strings.TrimSpace(raw)
	format := "text_or_code"
	switch {
	case trimmed == "":
		format = "empty"
	case strings.HasPrefix(trimmed, "```"):
		format = "markdown"
	case strings.HasPrefix(trimmed, "{"):
		format = "json_object"
	case strings.HasPrefix(trimmed, "["):
		format = "json_array"
	case strings.HasPrefix(trimmed, `"`):
		format = "json_string"
	}
	detail := fmt.Sprintf("format=%s; bytes=%d", format, len(raw))
	var syntax *json.SyntaxError
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); errors.As(err, &syntax) {
		detail += fmt.Sprintf("; json_offset=%d; json_failure=%s", syntax.Offset, openAIBPSJSONFailureKind(raw, syntax))
	}
	return detail
}

// openAIBPSJSONFailureKind 只分类解析器的结构上下文，不回显解析器消息（消息
// 里可能带客户端代码的一个字节）。
func openAIBPSJSONFailureKind(raw string, syntax *json.SyntaxError) string {
	if syntax == nil {
		return "unknown"
	}
	message := syntax.Error()
	if syntax.Offset > 0 && syntax.Offset <= int64(len(raw)) && raw[syntax.Offset-1] < 0x20 {
		return "raw_control"
	}
	if syntax.Offset > 1 && raw[syntax.Offset-2] == '\\' {
		return "invalid_escape"
	}
	if syntax.Offset > 5 && raw[syntax.Offset-6] == '\\' && raw[syntax.Offset-5] == 'u' {
		return "invalid_escape"
	}
	switch {
	case strings.Contains(message, "unexpected end of JSON input"):
		return "unexpected_eof"
	case strings.Contains(message, "after top-level value"):
		return "trailing_data"
	case strings.Contains(message, "in string escape code"), strings.Contains(message, "in \\u hexadecimal character escape"):
		return "invalid_escape"
	case strings.Contains(message, "in string literal"):
		if syntax.Offset > 0 && syntax.Offset <= int64(len(raw)) && raw[syntax.Offset-1] < 0x20 {
			return "raw_control"
		}
		return "invalid_string"
	case strings.Contains(message, "after object key"), strings.Contains(message, "after array element"):
		return "missing_separator"
	default:
		return "unexpected_token"
	}
}

// openAIBPSEnvelopeName 读取信封里的工具名，兼容 name/tool 别名。
func openAIBPSEnvelopeName(envelope map[string]any) (string, error) {
	name := stringValue(envelope["name"])
	alias := stringValue(envelope["tool"])
	if name != "" && alias != "" && name != alias {
		return "", errors.New("basispoints tool envelope contains conflicting names")
	}
	if name == "" {
		name = alias
	}
	return name, nil
}

// openAIBPSEnvelopeArguments 读取信封里的参数，兼容 arguments/args 别名。
func openAIBPSEnvelopeArguments(envelope map[string]any) (any, error) {
	args, exists := envelope["arguments"]
	alias, hasAlias := envelope["args"]
	if exists && hasAlias {
		return nil, errors.New("basispoints tool envelope contains conflicting argument fields")
	}
	if !exists {
		args = alias
	}
	return args, nil
}

func openAIBPSProsePrefix(prefix string) bool {
	return len(prefix) <= 512 && !strings.ContainsAny(prefix, "{}[]();=`\"")
}

func openAIBPSDecodeEnvelopeValue(raw string) (any, bool) {
	var value any
	if openAIBPSDecode([]byte(raw), &value) == nil {
		return value, true
	}
	// 只在严格解码失败后修复字符串里的裸换行/制表符与非法反斜杠转义，保留
	// 这些字符的原文；绝不猜测缺失的引号、分隔符或括号。
	fixed := openAIBPSRepairTransportJSONStrings(raw)
	if fixed != raw && openAIBPSDecode([]byte(fixed), &value) == nil {
		return value, true
	}
	return nil, false
}

func openAIBPSRepairTransportJSONStrings(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	quoted := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' {
			quoted = !quoted
		}
		if quoted {
			switch ch {
			case '\n':
				_, _ = out.WriteString(`\n`)
				continue
			case '\r':
				_, _ = out.WriteString(`\r`)
				continue
			case '\t':
				_, _ = out.WriteString(`\t`)
				continue
			}
		}
		if ch != '\\' || !quoted || i+1 >= len(raw) {
			_ = out.WriteByte(ch)
			continue
		}
		next := raw[i+1]
		valid := strings.ContainsRune(`"\/bfnrt`, rune(next))
		if next == 'u' && i+5 < len(raw) {
			valid = true
			for _, digit := range raw[i+2 : i+6] {
				if !strings.ContainsRune("0123456789abcdefABCDEF", digit) {
					valid = false
				}
			}
		}
		_ = out.WriteByte('\\')
		if valid {
			_ = out.WriteByte(next)
			i++
		} else {
			_ = out.WriteByte('\\')
		}
	}
	return out.String()
}
