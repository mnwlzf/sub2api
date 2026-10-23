package service

// 本文件复刻官方 Codex 客户端 turn metadata 的 serde 线序。
// 参考实现：sub4api（huoji120/sub4api，LGPL-3.0）的
// backend/internal/service/openai_codex_turn_metadata.go，其依据为
// codex-rs core/src/responses_metadata.rs 与 utils/string/src/json.rs。
//
// 本地此前用 encoding/json 直接序列化 map，键序恒为字母序；官方 serde 按
// struct 声明序输出、未知键走 flatten(BTreeMap) 字典序，两者字节不同，上游
// 可据此判定请求并非官方客户端产生。本实现替换原 map 序列化，所有调用点
// （HTTP 头、client_metadata、WS 帧）自动获得逐字节一致的线序。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf16"
)

// codexTurnMetadataKeyOrder 复刻官方 CodexTurnMetadataPayload 的 serde 字段顺序
// （codex-rs/core/src/responses_metadata.rs:544-601，全部 skip_serializing_if=none）。
// serde 按 struct 声明顺序序列化，未知键走 #[serde(flatten)] 的 BTreeMap<String,String>
// （字典序、排在末尾）。Go map 的 json.Marshal 恒为字母序，与官方线序不一致，
// 上游可逐字节比对——必须显式复刻。
var codexTurnMetadataKeyOrder = [...]string{
	"installation_id",
	"session_id",
	"thread_id",
	"agent_name",
	"turn_id",
	"window_id",
	"window_number",
	"context_window_id",
	"request_kind",
	"history_initialization",
	"forked_from_thread_id",
	"forked_from_ordinal_exclusive",
	"parent_thread_id",
	"parent_turn_id",
	"root_turn_id",
	"subagent_kind",
	"thread_source",
	"turn_trigger",
	"sandbox",
	"sandbox_mode",
	"auto_review_enabled",
	"node_repl_auto_review_required",
	"node_repl_disabled",
	"workspaces",
	"tool_namespaces_info",
	"turn_started_at_unix_ms",
	"history_ingest_requested",
	"analytics_enabled",
	"compaction",
}

// codexJSONShape 描述一类官方 JSON 对象的线序形状。serde 语义下：
//   - order 非空：该层是 struct，键按 order（声明序）输出，order 之外的未知键
//     按字典序追加（复刻 flatten 的 BTreeMap）。
//   - order 为空：该层是 BTreeMap，键按字典序输出。
//   - fields：struct 内某键指向的子对象 kind（缺省为叶子）。
//   - values：BTreeMap 的每个 value 指向的子对象 kind（缺省为叶子）。
type codexJSONShape struct {
	order  []string
	fields map[string]string
	values string
}

// codexJSONShapes 复刻官方 turn metadata 里出现的各类对象形状。
// 依据 codex-rs core/src/responses_metadata.rs（payload 544-601、workspace 186-194、
// tool namespace 196-222）与 analytics/src/facts.rs（compaction 枚举）。
var codexJSONShapes = map[string]codexJSONShape{
	"": { // CodexTurnMetadataPayload 顶层
		order: codexTurnMetadataKeyOrder[:],
		fields: map[string]string{
			"workspaces":           "workspaces",
			"tool_namespaces_info": "tool_namespaces_info",
			"compaction":           "compaction",
		},
	},
	"workspaces":           {values: "workspace"}, // BTreeMap<path, TurnMetadataWorkspace>
	"workspace":            {order: []string{"associated_remote_urls", "latest_git_commit_hash", "has_changes"}, fields: map[string]string{"associated_remote_urls": "remote_urls"}},
	"remote_urls":          {},                    // BTreeMap<name, SanitizedGitUrl(String)>
	"tool_namespaces_info": {values: "namespace"}, // BTreeMap<name, TurnToolNamespaceInfo>
	"namespace":            {order: []string{"name", "functions"}, fields: map[string]string{"functions": "functions"}},
	"functions":            {values: "function"}, // BTreeMap<name, TurnToolFunctionInfo>
	"function":             {order: []string{"name", "direct", "code_mode_name", "deferred", "source"}},
	"compaction":           {order: []string{"trigger", "reason", "implementation", "phase", "strategy"}},
}

// orderedCodexJSONKeys 先按官方顺序取已出现的键，再把未知键按字典序追加，
// 复刻 serde struct + flatten(BTreeMap) 的混合输出。
func orderedCodexJSONKeys(object map[string]any, order []string) []string {
	keys := make([]string, 0, len(object))
	seen := make(map[string]struct{}, len(order))
	for _, key := range order {
		if _, exists := object[key]; exists {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	rest := make([]string, 0, len(object)-len(seen))
	for key := range object {
		if _, ok := seen[key]; !ok {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// writeCodexJSONString 只转义 JSON 语法必需的字节；非 ASCII 留给
// escapeCodexNonASCII 统一处理（官方 AsciiJsonFormatter 也是两层语义）。
// 与官方一致：& < > 不转义。
func writeCodexJSONString(buf *bytes.Buffer, value string) {
	buf.WriteByte('"')
	start := 0
	for i := range len(value) {
		switch b := value[i]; b {
		case '"', '\\':
			buf.WriteString(value[start:i])
			buf.WriteByte('\\')
			buf.WriteByte(b)
			start = i + 1
		case '\n':
			buf.WriteString(value[start:i])
			buf.WriteString(`\n`)
			start = i + 1
		case '\r':
			buf.WriteString(value[start:i])
			buf.WriteString(`\r`)
			start = i + 1
		case '\t':
			buf.WriteString(value[start:i])
			buf.WriteString(`\t`)
			start = i + 1
		default:
			if b < 0x20 {
				buf.WriteString(value[start:i])
				fmt.Fprintf(buf, `\u%04x`, b)
				start = i + 1
			}
		}
	}
	buf.WriteString(value[start:])
	buf.WriteByte('"')
}

// writeCodexJSONLeaf 用关闭 HTML 转义的编码器输出标量与数组。
func writeCodexJSONLeaf(buf *bytes.Buffer, value any) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	out := encoded.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	buf.Write(out)
	return nil
}

func writeCodexJSONObject(buf *bytes.Buffer, object map[string]any, kind string) error {
	shape := codexJSONShapes[kind]
	keys := orderedCodexJSONKeys(object, shape.order)
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeCodexJSONString(buf, key)
		buf.WriteByte(':')
		child, ok := object[key].(map[string]any)
		if !ok {
			if err := writeCodexJSONLeaf(buf, object[key]); err != nil {
				return err
			}
			continue
		}
		childKind := shape.fields[key]
		if childKind == "" {
			childKind = shape.values
		}
		if err := writeCodexJSONObject(buf, child, childKind); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// escapeCodexNonASCII 复刻官方 to_ascii_json_string 的非 ASCII 转义
// （codex-rs/utils/string/src/json.rs:12-38）：只把非 ASCII 码点写成小写 \uXXXX，
// ASCII（含 & < > 与 U+007F）原样保留。官方仅在字符串片段上做该转义，
// 而合法 JSON 里的高位字节只可能出现在字符串内，故字节级后处理等价。
func escapeCodexNonASCII(raw []byte) []byte {
	for i, b := range raw {
		if b < 0x7f {
			continue
		}
		out := make([]byte, 0, len(raw)+16)
		out = append(out, raw[:i]...)
		appendEscape := func(r rune) {
			const hex = "0123456789abcdef"
			out = append(out, '\\', 'u', hex[r>>12&15], hex[r>>8&15], hex[r>>4&15], hex[r&15])
		}
		for _, r := range string(raw[i:]) {
			switch {
			case r < 0x7f:
				out = append(out, byte(r))
			case r <= 0xffff:
				appendEscape(r)
			default:
				high, low := utf16.EncodeRune(r)
				appendEscape(high)
				appendEscape(low)
			}
		}
		return out
	}
	return raw
}

func marshalCodexTurnMetadata(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	if err := writeCodexJSONObject(&buf, metadata, ""); err != nil {
		return nil, err
	}
	return escapeCodexNonASCII(buf.Bytes()), nil
}
