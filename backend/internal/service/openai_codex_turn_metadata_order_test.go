package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 官方 CodexTurnMetadataPayload 是 serde struct：键按声明序输出，未知键走
// flatten(BTreeMap) 的字典序排在末尾。Go map 默认字母序会把 turn_id 排到
// session_id 之前，与官方线序不同——这里锁住顺序与转义语义。
func TestMarshalCodexTurnMetadataUsesOfficialKeyOrder(t *testing.T) {
	metadata := map[string]any{
		"turn_id":                 "turn",
		"session_id":              "session",
		"thread_id":               "thread",
		"installation_id":         "install",
		"window_id":               "window",
		"turn_started_at_unix_ms": int64(1789302780858),
		"zzz_custom":              "custom",
		"aaa_custom":              "custom",
	}

	raw, err := marshalCodexTurnMetadata(metadata)
	require.NoError(t, err)
	encoded := string(raw)

	ordered := []string{
		`"installation_id"`,
		`"session_id"`,
		`"thread_id"`,
		`"turn_id"`,
		`"window_id"`,
		`"turn_started_at_unix_ms"`,
	}
	position := -1
	for _, key := range ordered {
		index := strings.Index(encoded, key)
		require.Greater(t, index, position, "key %s must follow official serde order", key)
		position = index
	}
	// 未知键按字典序追加在已知键之后。
	require.Less(t, strings.Index(encoded, `"zzz_custom"`), len(encoded))
	require.Greater(t, strings.Index(encoded, `"aaa_custom"`), strings.Index(encoded, `"turn_started_at_unix_ms"`))
	require.Less(t, strings.Index(encoded, `"aaa_custom"`), strings.Index(encoded, `"zzz_custom"`))
}

// 官方 AsciiJsonFormatter 只把非 ASCII 转成小写 \uXXXX；& < > 不转义。
func TestMarshalCodexTurnMetadataEscaping(t *testing.T) {
	raw, err := marshalCodexTurnMetadata(map[string]any{
		"session_id": "会话🚀",
		"note":       "a&b<c>d",
	})
	require.NoError(t, err)
	encoded := string(raw)

	require.Contains(t, encoded, `\u4f1a\u8bdd\ud83d\ude80`, "非 ASCII 使用小写 \\uXXXX 且代理对拆分")
	require.Contains(t, encoded, `"a&b<c>d"`, "& < > 不转义")
	require.NotContains(t, encoded, `\u0026`)
	require.NotContains(t, encoded, `\u003c`)
}
