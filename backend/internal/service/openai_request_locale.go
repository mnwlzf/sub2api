package service

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// openAIEnvironmentFieldStart 只解析环境上下文的字段边界。环境文本是类 XML 的，
// 但 subagents、network 等字段可能包含未转义文本，对这些字段整体不透明。
var openAIEnvironmentFieldStart = regexp.MustCompile(`^<([A-Za-z_][A-Za-z0-9_.:-]*)(?:[ \t\r\n]+(?:[^<>"']|"[^"]*"|'[^']*')*)?[ \t\r\n]*/?>`)

// findOpenAIEnvironmentFieldEnd 查找字段的闭合标签。闭合标签的 > 前可能有空白；
// 只匹配字段名，嵌套字段与字段体中的未转义字符保持不透明。
func findOpenAIEnvironmentFieldEnd(text, name string) (start, end int) {
	prefix := "</" + name
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], prefix)
		if index < 0 {
			break
		}
		start = offset + index
		offset = start + len(prefix)
		rest := strings.TrimLeft(text[offset:], " \t\r\n")
		if strings.HasPrefix(rest, ">") {
			return start, len(text) - len(rest) + 1
		}
	}
	return -1, -1
}

func rewriteOpenAIRequestEnvironment(text, timezone string) (rewritten, previous, reason string) {
	return rewriteOpenAIRequestEnvironmentAt(text, timezone, "", false)
}

// rewriteOpenAIRequestEnvironmentAt 只改写完整 environment_context 块内的 timezone，
// 并在缺失时补齐 current_date；已存在的 current_date 及其它字段原样保留（1.1.4 修正：
// 仅改账号时区，保留客户端原始日期、时间与其他内容）。fillMissing=false 时仅替换
// 已存在的 timezone，不做任何插入。
func rewriteOpenAIRequestEnvironmentAt(text, timezone, currentDate string, fillMissing bool) (rewritten, previous, reason string) {
	trimmed := strings.TrimSpace(text)
	root := openAIEnvironmentFieldStart.FindStringSubmatchIndex(trimmed)
	if root == nil || trimmed[root[2]:root[3]] != "environment_context" {
		return text, "", "no_environment_context"
	}
	rootCloseStart, rootCloseEnd := findOpenAIEnvironmentFieldEnd(trimmed[root[1]:], "environment_context")
	if strings.HasSuffix(trimmed[:root[1]], "/>") || rootCloseEnd != len(trimmed)-root[1] {
		return text, "", "invalid_environment_context"
	}
	content := trimmed[root[1] : root[1]+rootCloseStart]
	valueStart, valueEnd := -1, -1
	hasCurrentDate := false
	for offset := 0; offset < len(content); {
		rest := strings.TrimLeftFunc(content[offset:], unicode.IsSpace)
		offset = len(content) - len(rest)
		if rest == "" {
			break
		}
		field := openAIEnvironmentFieldStart.FindStringSubmatchIndex(rest)
		if field == nil {
			return text, "", "invalid_environment_context"
		}
		name := rest[field[2]:field[3]]
		if name == "environment_context" {
			return text, "", "invalid_environment_context"
		}
		selfClosing := strings.HasSuffix(rest[:field[1]], "/>")
		if name == "timezone" && (valueStart >= 0 || selfClosing) {
			return text, "", "invalid_environment_context"
		}
		offset += field[1]
		if selfClosing {
			if name == "current_date" {
				hasCurrentDate = true
			}
			continue
		}
		end, closeEnd := findOpenAIEnvironmentFieldEnd(content[offset:], name)
		if end < 0 {
			return text, "", "invalid_environment_context"
		}
		if name != "timezone" && strings.Contains(content[offset:offset+end], "<timezone") {
			return text, "", "invalid_environment_context"
		}
		switch name {
		case "current_date":
			hasCurrentDate = true
		case "timezone":
			value := content[offset : offset+end]
			previous = strings.TrimSpace(value)
			if previous == "" || strings.Contains(value, "<") {
				return text, "", "invalid_environment_context"
			}
			valueStart = offset + len(value) - len(strings.TrimLeftFunc(value, unicode.IsSpace))
			valueEnd = offset + len(strings.TrimRightFunc(value, unicode.IsSpace))
		}
		offset += closeEnd
	}
	if !fillMissing {
		if valueStart < 0 {
			return text, "", "no_timezone"
		}
		if previous == timezone {
			return text, previous, "already_target"
		}
		leading := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
		base := leading + root[1]
		return text[:base+valueStart] + timezone + text[base+valueEnd:], previous, "replaced"
	}
	if currentDate == "" {
		currentDate = time.Now().In(openAIRequestLocation(timezone)).Format(time.DateOnly)
	}
	if valueStart < 0 || !hasCurrentDate {
		leading := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
		if valueStart >= 0 && previous != timezone {
			base := leading + root[1]
			text = text[:base+valueStart] + timezone + text[base+valueEnd:]
			trimmed = strings.TrimSpace(text)
			root = openAIEnvironmentFieldStart.FindStringSubmatchIndex(trimmed)
			rootCloseStart, rootCloseEnd = findOpenAIEnvironmentFieldEnd(trimmed[root[1]:], "environment_context")
			content = trimmed[root[1] : root[1]+rootCloseStart]
		}
		if valueStart < 0 || !hasCurrentDate {
			insert := ""
			if !hasCurrentDate {
				insert += "\n  <current_date>" + currentDate + "</current_date>"
			}
			if valueStart < 0 {
				insert += "\n  <timezone>" + timezone + "</timezone>"
			}
			insert += "\n"
			trimmed = trimmed[:root[1]+rootCloseStart] + insert + trimmed[root[1]+rootCloseStart:]
			return text[:leading] + trimmed + text[leading+len(strings.TrimSpace(text)):], previous, "inserted"
		}
	}
	if previous == timezone {
		return text, previous, "already_target"
	}
	leading := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
	base := leading + root[1]
	return text[:base+valueStart] + timezone + text[base+valueEnd:], previous, "replaced"
}

func openAIRequestLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

// openAIRequestPersona 描述一个时区对应的地理位置掩护。官方 web_search 的
// user_location 带 country/region/city/timezone 四个字段，只改 timezone 会让客户端
// 真实位置与伪装时区自相矛盾。未登记的时区返回 nil，此时只改 timezone（不臆造国家）。
type openAIRequestPersona struct {
	location map[string]string
}

var openAIRequestPersonas = map[string]openAIRequestPersona{
	"Asia/Singapore": {location: map[string]string{
		"country": "SG",
		"region":  "Central Region",
		"city":    "Singapore",
	}},
}

func openAIRequestPersonaForTimezone(timezone string) *openAIRequestPersona {
	if persona, ok := openAIRequestPersonas[timezone]; ok {
		return &persona
	}
	return nil
}

// NormalizeOpenAIRequestLocale 按账号级请求时区改写发往 OpenAI 上游的请求体。
//
// 只改写明确标记为环境上下文的内容与 web_search 工具的 user_location：
//   - environment_context 内已有的 timezone 替换为目标时区；已有的 current_date
//     与其它字段保留客户端原始内容，缺失的 current_date 才按目标时区补齐；
//   - web_search 工具 user_location 中已存在的 country/region/city/timezone
//     四个字段改写为目标时区对应的 persona，不凭空新增字段；未登记时区只改 timezone。
//
// 普通对话文本与其它字段一律不动；非法 JSON、非 OpenAI 账号原样返回。
// transport 仅用于日志区分 HTTP / WebSocket，行为一致。
func NormalizeOpenAIRequestLocale(ctx context.Context, account *Account, body []byte, transport string) []byte {
	if account == nil || !account.IsOpenAI() {
		return body
	}
	timezone := account.GetOpenAIRequestTimezone()
	currentDate := time.Now().In(openAIRequestLocation(timezone)).Format(time.DateOnly)
	before := make([]*string, 0)
	after := make([]*string, 0)
	matched := 0
	replaced := 0
	inserted := 0
	invalidEnvironment := false
	rewriteText := func(path string, text gjson.Result) {
		if text.Type != gjson.String {
			return
		}
		next, previous, reason := rewriteOpenAIRequestEnvironmentAt(text.String(), timezone, currentDate, true)
		if reason == "no_environment_context" {
			return
		}
		matched++
		var prior, current *string
		switch reason {
		case "invalid_environment_context":
			invalidEnvironment = true
		case "inserted", "replaced", "already_target":
			loggedPrevious := previous
			if len(loggedPrevious) > 128 {
				loggedPrevious = loggedPrevious[:128]
			}
			prior, current = &loggedPrevious, &loggedPrevious
			if reason == "inserted" || reason == "replaced" {
				if updated, err := sjson.SetBytes(body, path, next); err == nil {
					body = updated
					current = &timezone
					if reason == "inserted" {
						inserted++
					} else {
						replaced++
					}
				} else {
					invalidEnvironment = true
				}
			}
		}
		before = append(before, prior)
		after = append(after, current)
	}
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for inputIndex, item := range input.Array() {
			if item.Get("role").String() != "user" {
				continue
			}
			content := item.Get("content")
			path := "input." + strconv.Itoa(inputIndex) + ".content"
			if content.Type == gjson.String {
				rewriteText(path, content)
			} else if content.IsArray() {
				for contentIndex, part := range content.Array() {
					if part.Get("type").String() == "input_text" {
						rewriteText(path+"."+strconv.Itoa(contentIndex)+".text", part.Get("text"))
					}
				}
			}
		}
	}
	webSearchBefore := make([]*string, 0)
	webSearchAfter := make([]*string, 0)
	tools := gjson.GetBytes(body, "tools")
	persona := openAIRequestPersonaForTimezone(timezone)
	if tools.IsArray() {
		for index, tool := range tools.Array() {
			kind := tool.Get("type").String()
			if kind != "web_search" && !strings.HasPrefix(kind, "web_search_") {
				continue
			}
			value := tool.Get("user_location.timezone")
			if value.Type == gjson.String {
				previous := value.String()
				loggedPrevious := previous
				if len(loggedPrevious) > 128 {
					loggedPrevious = loggedPrevious[:128]
				}
				current := loggedPrevious
				if previous != timezone {
					if updated, err := sjson.SetBytes(body, "tools."+strconv.Itoa(index)+".user_location.timezone", timezone); err == nil {
						body = updated
						current = timezone
						replaced++
					}
				}
				// 官方 user_location 还带 country/region/city，只改 timezone 会让客户端
				// 真实国家/城市原样外泄，与伪装的时区自相矛盾；仅改写已存在的字段。
				if persona != nil {
					for field, personaValue := range persona.location {
						key := "tools." + strconv.Itoa(index) + ".user_location." + field
						if fieldValue := tool.Get("user_location." + field); fieldValue.Type == gjson.String && fieldValue.String() != personaValue {
							if updated, err := sjson.SetBytes(body, key, personaValue); err == nil {
								body = updated
								replaced++
							}
						}
					}
				}
				webSearchBefore = append(webSearchBefore, &loggedPrevious)
				webSearchAfter = append(webSearchAfter, &current)
			}
		}
	}
	reason := "no_environment_context"
	if replaced > 0 {
		reason = "replaced"
	} else if inserted > 0 {
		reason = "inserted"
	} else if invalidEnvironment {
		reason = "invalid_environment_context"
	} else if matched > 0 {
		reason = "no_timezone"
		for _, value := range before {
			if value != nil {
				reason = "already_target"
				break
			}
		}
	}
	logger.FromContext(ctx).Debug("openai request timezone normalization",
		zap.Int64("account_id", account.ID), zap.String("transport", transport),
		zap.Bool("timezone_replaced", replaced > 0), zap.Bool("environment_fields_inserted", inserted > 0), zap.Any("timezone_before", before),
		zap.Any("timezone_after", after), zap.Any("web_search_timezone_before", webSearchBefore),
		zap.Any("web_search_timezone_after", webSearchAfter), zap.String("target_timezone", timezone),
		zap.Int("matched_count", matched), zap.Int("replaced_count", replaced), zap.Int("inserted_count", inserted), zap.String("reason", reason))
	return body
}
