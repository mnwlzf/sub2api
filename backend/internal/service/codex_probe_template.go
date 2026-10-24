package service

import (
	"context"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CodexProbeTemplateMaxBytes 是可编辑探测模板的体积上限。
const CodexProbeTemplateMaxBytes = 256 << 10

// CodexProbePromptPlaceholder 是模板中唯一允许注入 ModelTrace 挑战的位置。
const CodexProbePromptPlaceholder = "{{MODELTRACE_PROMPT}}"

// defaultCodexProbeTimezone 与真实 Codex 客户端的默认时区保持一致。
const defaultCodexProbeTimezone = "Asia/Singapore"

//go:embed codex_probe_template.jsonl
var defaultCodexProbeTemplateJSONL string

var (
	// ErrCodexProbeTemplateInvalid 表示探测模板不可解析/不符合初始上下文格式。
	ErrCodexProbeTemplateInvalid = errors.New("invalid Codex probe template")
	// ErrCodexProbeIdentity 表示无法为目标账号构造 Codex 身份标识。
	ErrCodexProbeIdentity = errors.New("codex probe identity unavailable")
)

// CodexProbeTemplate 是不可变、已校验的 Codex 初始会话模板。
// 可被并发请求共享；渲染只读取存储的字符串，不会修改模板本身。
type CodexProbeTemplate struct {
	instructions string
	messages     []codexProbeMessage
}

type codexProbeText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexProbeMessage struct {
	Type    string           `json:"type"`
	Role    string           `json:"role"`
	Content []codexProbeText `json:"content"`
}

// DefaultCodexProbeTemplate 返回内嵌的默认 JSONL 模板（7 行）。
func DefaultCodexProbeTemplate() string { return defaultCodexProbeTemplateJSONL }

// EffectiveCodexProbeTemplate 在设置为空时回退到内嵌默认模板。
func EffectiveCodexProbeTemplate(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return DefaultCodexProbeTemplate()
	}
	return raw
}

func decodeCodexProbeJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

var codexProbePlaceholder = regexp.MustCompile(`\{\{[^{}]*\}\}`)

func validateCodexProbePlaceholders(text string) bool {
	valid := true
	rest := codexProbePlaceholder.ReplaceAllStringFunc(text, func(value string) string {
		switch value {
		case "{{TIMEZONE}}", "{{CURRENT_DATE}}", "{{MODEL}}", CodexProbePromptPlaceholder:
		default:
			valid = false
		}
		return ""
	})
	return valid && !strings.Contains(rest, "{{") && !strings.Contains(rest, "}}")
}

// Only environment_context is XML. Other wrappers, notably <permissions
// instructions>, are literal prompt delimiters and must not be normalized.
func codexProbeEnvironmentShape(text string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	var shape strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return shape.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch token := token.(type) {
		case xml.StartElement:
			attrs := make([]string, 0, len(token.Attr))
			for _, attr := range token.Attr {
				attrs = append(attrs, fmt.Sprintf("%v=%q", attr.Name, attr.Value))
			}
			sort.Strings(attrs)
			fmt.Fprintf(&shape, "+%v%v;", token.Name, attrs)
		case xml.EndElement:
			fmt.Fprintf(&shape, "-%v;", token.Name)
		case xml.Directive, xml.ProcInst:
			return "", errors.New("unexpected XML directive")
		}
	}
}

var defaultCodexProbeEnvironmentShape = sync.OnceValue(func() string {
	var record struct {
		Payload codexProbeMessage `json:"payload"`
	}
	lines := strings.Split(DefaultCodexProbeTemplate(), "\n")
	if err := json.Unmarshal([]byte(lines[4]), &record); err != nil {
		panic("invalid embedded Codex probe environment")
	}
	shape, err := codexProbeEnvironmentShape(record.Payload.Content[0].Text)
	if err != nil {
		panic("invalid embedded Codex probe environment XML")
	}
	return shape
})

// ParseCodexProbeTemplate 只接受"初始上下文"格式，拒绝原始录制里的标识符、
// 推理内容、assistant 输出或工具事件。错误只报告行号与规则，绝不回显提交的
// 提示词文本。
func ParseCodexProbeTemplate(raw string) (*CodexProbeTemplate, error) {
	if len(raw) > CodexProbeTemplateMaxBytes {
		return nil, fmt.Errorf("%w: line 1: maximum size is 256 KiB", ErrCodexProbeTemplateInvalid)
	}
	raw = EffectiveCodexProbeTemplate(raw)
	template := &CodexProbeTemplate{}
	types := []string{"session_meta", "response_item", "response_item", "response_item", "response_item", "turn_context", "response_item"}
	roles := map[int]string{1: "developer", 2: "developer", 3: "developer", 4: "user", 6: "user"}
	parts := map[int]int{1: 5, 2: 1, 3: 1, 4: 1, 6: 1}
	index, placeholders := 0, 0
	lastLine := 1
	for line, rawLine := range strings.Split(raw, "\n") {
		lastLine = line + 1
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		fail := func(reason string) (*CodexProbeTemplate, error) {
			return nil, fmt.Errorf("%w: line %d: %s", ErrCodexProbeTemplateInvalid, line+1, reason)
		}
		var record struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if decodeCodexProbeJSON(rawLine, &record) != nil {
			return fail("expected a JSON object containing only type and payload")
		}
		if index >= len(types) || record.Type != types[index] {
			return fail("unexpected record type or initial-context order")
		}
		switch record.Type {
		case "session_meta":
			var payload struct {
				Originator string `json:"originator"`
				Base       struct {
					Text string `json:"text"`
				} `json:"base_instructions"`
			}
			if decodeCodexProbeJSON(string(record.Payload), &payload) != nil || payload.Originator != "Codex Desktop" || strings.TrimSpace(payload.Base.Text) == "" {
				return fail("expected Codex Desktop originator and nonempty base_instructions.text")
			}
			if !validateCodexProbePlaceholders(payload.Base.Text) {
				return fail("unknown or malformed placeholder")
			}
			placeholders += strings.Count(payload.Base.Text, CodexProbePromptPlaceholder)
			template.instructions = payload.Base.Text
		case "turn_context":
			var payload struct {
				Date     string `json:"current_date"`
				Timezone string `json:"timezone"`
				Model    string `json:"model"`
			}
			if decodeCodexProbeJSON(string(record.Payload), &payload) != nil || payload.Date != "{{CURRENT_DATE}}" || payload.Timezone != "{{TIMEZONE}}" || payload.Model != "{{MODEL}}" {
				return fail("turn_context requires CURRENT_DATE, TIMEZONE and MODEL placeholders")
			}
		case "response_item":
			var payload codexProbeMessage
			if decodeCodexProbeJSON(string(record.Payload), &payload) != nil || payload.Type != "message" || payload.Role != roles[index] || len(payload.Content) != parts[index] {
				return fail("retain the initial developer/user message roles and content parts")
			}
			message := codexProbeMessage{Role: payload.Role}
			for partIndex, part := range payload.Content {
				if part.Type != "input_text" || strings.TrimSpace(part.Text) == "" {
					return fail("each content part must be nonempty input_text")
				}
				if !validateCodexProbePlaceholders(part.Text) {
					return fail("unknown or malformed placeholder")
				}
				placeholders += strings.Count(part.Text, CodexProbePromptPlaceholder)
				var tag string
				switch index {
				case 1:
					tag = []string{"app-context", "", "skills_instructions", "permissions instructions", "collaboration_mode"}[partIndex]
					if partIndex == 1 && !strings.HasPrefix(part.Text, "## Memory\n") {
						return fail("retain the Memory heading")
					}
				case 2:
					tag = "multi_agent_role"
				case 3:
					tag = "multi_agent_mode"
				case 4:
					tag = "environment_context"
					shape, err := codexProbeEnvironmentShape(part.Text)
					var env struct {
						Date     string `xml:"current_date"`
						Timezone string `xml:"timezone"`
					}
					if err != nil || shape != defaultCodexProbeEnvironmentShape() || xml.Unmarshal([]byte(part.Text), &env) != nil {
						return fail("retain the environment XML hierarchy and attributes")
					}
					if strings.TrimSpace(env.Date) != "{{CURRENT_DATE}}" || strings.TrimSpace(env.Timezone) != "{{TIMEZONE}}" {
						return fail("environment date and timezone must use their placeholders")
					}
				case 6:
					if part.Text != CodexProbePromptPlaceholder {
						return fail("final user message must contain only the challenge placeholder")
					}
				}
				if tag != "" && (!strings.HasPrefix(strings.TrimSpace(part.Text), "<"+tag+">") || strings.Count(part.Text, "<"+tag+">") != 1 || strings.Count(part.Text, "</"+tag+">") != 1) {
					return fail("retain the original prompt wrapper tags")
				}
				message.Content = append(message.Content, part)
			}
			template.messages = append(template.messages, message)
		}
		index++
	}
	if index != len(types) || placeholders != 1 {
		return nil, fmt.Errorf("%w: line %d: expected seven initial records and exactly one challenge placeholder", ErrCodexProbeTemplateInvalid, lastLine)
	}
	return template, nil
}

var parsedDefaultCodexProbeTemplate = sync.OnceValues(func() (*CodexProbeTemplate, error) {
	return ParseCodexProbeTemplate(DefaultCodexProbeTemplate())
})

// codexProbeIdentity 是一次探测请求的 Codex 身份集合。真实 Codex 客户端每次
// turn 都会生成新的 session/window/turn 标识；installation_id 在账号维度稳定。
type codexProbeIdentity struct {
	installationID  string
	sessionID       string
	threadID        string
	turnID          string
	windowID        string
	contextWindowID string
	startedAt       time.Time
}

func newCodexProbeIdentity(installationID string, now time.Time) (codexProbeIdentity, error) {
	installationID = strings.TrimSpace(installationID)
	if installationID == "" {
		return codexProbeIdentity{}, ErrCodexProbeIdentity
	}
	identity := codexProbeIdentity{installationID: installationID, startedAt: now}
	newID := func() (string, error) {
		id, err := uuid.NewV7()
		if err != nil {
			return "", err
		}
		return id.String(), nil
	}
	sessionID, err := newID()
	if err != nil {
		return codexProbeIdentity{}, err
	}
	identity.sessionID = sessionID
	identity.threadID = sessionID
	turnID, err := newID()
	if err != nil {
		return codexProbeIdentity{}, err
	}
	identity.turnID = turnID
	contextWindowID, err := newID()
	if err != nil {
		return codexProbeIdentity{}, err
	}
	identity.contextWindowID = contextWindowID
	// 官方 window_id 恒为 <thread-uuid>:<ordinal>，不是独立 UUID。
	identity.windowID = identity.threadID + ":0"
	return identity, nil
}

// codexProbeTimezoneForAccount 选择账号的请求时区；非法值回退默认时区。
func codexProbeTimezoneForAccount(account *Account) string {
	if account != nil {
		candidates := []string{
			account.GetCredential("timezone"),
			account.GetCredential("openai_request_timezone"),
		}
		if account.Extra != nil {
			if raw, ok := account.Extra["openai_request_timezone"].(string); ok {
				candidates = append(candidates, raw)
			}
		}
		for _, candidate := range candidates {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if _, err := time.LoadLocation(candidate); err == nil {
				return candidate
			}
		}
	}
	return defaultCodexProbeTimezone
}

// renderCodexProbeRequest 把模板渲染成 Codex /backend-api/codex/responses 的
// 请求体与身份头。prompt 中的占位符样式文本不会被递归展开。
func (template *CodexProbeTemplate) renderCodexProbeRequest(model, prompt, timezone string, identity codexProbeIdentity) ([]byte, http.Header, error) {
	if template == nil {
		return nil, nil, ErrCodexProbeTemplateInvalid
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, ErrCodexProbeIdentity
	}
	replace := strings.NewReplacer(
		"{{TIMEZONE}}", location.String(),
		"{{CURRENT_DATE}}", identity.startedAt.In(location).Format(time.DateOnly),
		"{{MODEL}}", model,
		CodexProbePromptPlaceholder, prompt,
	)
	metadata, err := marshalCodexTurnMetadata(map[string]any{
		"installation_id":         identity.installationID,
		"session_id":              identity.sessionID,
		"thread_id":               identity.threadID,
		"turn_id":                 identity.turnID,
		"window_id":               identity.windowID,
		"window_number":           0,
		"context_window_id":       identity.contextWindowID,
		"turn_started_at_unix_ms": identity.startedAt.UnixMilli(),
		"request_kind":            "turn",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal Codex turn metadata: %w", err)
	}
	input := make([]any, 0, len(template.messages))
	for _, message := range template.messages {
		content := make([]any, 0, len(message.Content))
		for _, part := range message.Content {
			content = append(content, map[string]string{"type": "input_text", "text": replace.Replace(part.Text)})
		}
		input = append(input, map[string]any{"role": message.Role, "content": content})
	}
	body := map[string]any{
		"model":            model,
		"store":            false,
		"stream":           true,
		"instructions":     replace.Replace(template.instructions),
		"input":            input,
		"prompt_cache_key": identity.sessionID,
		"client_metadata": map[string]string{
			"session_id":              identity.sessionID,
			"thread_id":               identity.threadID,
			"turn_id":                 identity.turnID,
			"x-codex-installation-id": identity.installationID,
			"x-codex-window-id":       identity.windowID,
			"x-codex-turn-metadata":   string(metadata),
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal Codex probe request: %w", err)
	}
	headers := http.Header{}
	for _, key := range []string{"session_id", "session-id", "thread-id", "x-client-request-id"} {
		headers.Set(key, identity.sessionID)
	}
	headers.Set("x-codex-installation-id", identity.installationID)
	headers.Set("x-codex-window-id", identity.windowID)
	headers.Set("x-codex-turn-metadata", string(metadata))
	return encoded, headers, nil
}

// CodexProbeIdentityForAccount 为账号派生稳定的 installation_id，用于构造
// 与真实 Codex 客户端一致的探测身份。
func newCodexProbeIdentityForAccount(account *Account, now time.Time) (codexProbeIdentity, error) {
	if account == nil || codexAccountIdentityNamespace(account) == "" {
		return codexProbeIdentity{}, ErrCodexProbeIdentity
	}
	installation := scopeCodexAccountIdentityValue(account, 0, "installation", "codex-probe-v1")
	if strings.TrimSpace(installation) == "" {
		return codexProbeIdentity{}, ErrCodexProbeIdentity
	}
	return newCodexProbeIdentity(installation, now)
}

// prepareCodexProbeTemplate 读取设置中的模板；未配置或非法时返回错误。
// 探测请求只有在模板校验通过后才会发出。
func prepareCodexProbeTemplate(ctx context.Context, settings *SettingService) (*CodexProbeTemplate, error) {
	if settings == nil {
		return parsedDefaultCodexProbeTemplate()
	}
	template, err := settings.GetCodexProbeTemplate(ctx)
	if err != nil {
		if errors.Is(err, ErrCodexProbeTemplateInvalid) {
			return nil, err
		}
		return nil, ErrCodexProbeTemplateInvalid
	}
	return template, nil
}
